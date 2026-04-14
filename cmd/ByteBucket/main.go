package main

import (
	"context"
	"encoding/base64"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"ByteBucket/internal/router"
	"ByteBucket/internal/storage"
)

// shutdownTimeout bounds how long in-flight requests get to drain before the
// process exits. 30s mirrors common orchestrator grace periods (k8s default is
// 30s terminationGracePeriodSeconds) so Shutdown will naturally lose the race
// to SIGKILL past this point — keeping it here means any leaked connection is
// visible as a shutdown error rather than a silent kill.
const shutdownTimeout = 30 * time.Second

// Per-server I/O bounds. These are deliberately conservative for a first pass:
//
//   - readHeaderTimeout: 10s is well above any well-behaved client and caps
//     slowloris header drips that would otherwise hold a goroutine open.
//   - readTimeout / writeTimeout: 5m is a naive per-connection bound that
//     covers S3 ops on small/medium objects over slow networks but will be
//     tight for very large single-object PUT/GET. Multipart upload (planned)
//     keeps each part well under this. Streaming per-operation deadlines are
//     the proper long-term fix and are flagged for a later refactor.
//   - idleTimeout: 120s caps keepalive churn without closing hot connections.
//   - maxHeaderBytes: 1 MiB matches Go's default but is set explicitly so a
//     drive-by change to net/http defaults cannot silently relax it.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 5 * time.Minute
	writeTimeout      = 5 * time.Minute
	idleTimeout       = 120 * time.Second
	maxHeaderBytes    = 1 << 20
)

// newServer applies the standard per-server bounds. Centralised so the two
// servers stay in lockstep and a future reviewer sees every timeout in one
// place rather than hunting for stray http.Server{} literals.
func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
}

// ensureDirectoriesExist checks and creates required directories at startup.
func ensureDirectoriesExist() error {
	requiredDirs := []string{"/data", "/data/objects"}
	for _, dir := range requiredDirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			log.Printf("Directory %s not found, creating...", dir)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return err
			}
		}
	}
	log.Println("Required directories are present.")
	return nil
}

func main() {
	// NotifyContext gives a single cancellable context that trips on either a
	// user-initiated Ctrl+C (SIGINT) or an orchestrator-initiated SIGTERM. The
	// returned stop releases signal handlers so a second signal aborts.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		log.Printf("server error: %v", err)
		os.Exit(1)
	}
}

// run owns the full server lifecycle. Split out from main so the shutdown path
// is unit-testable with a cancellable context — main() itself is not easily
// exercised under `go test`.
func run(ctx context.Context) error {
	if err := ensureDirectoriesExist(); err != nil {
		return err
	}

	encKey, err := loadEncryptionKey()
	if err != nil {
		return err
	}
	storage.SetEncryptionKey(encKey)

	if err := storage.InitUserStore("/data/users.db"); err != nil {
		return err
	}

	if err := bootstrapSuperUser(); err != nil {
		return err
	}

	storageSrv := newServer(":9000", router.NewStorageRouter())
	adminSrv := newServer(":9001", router.NewAdminRouter())
	return serve(ctx, storageSrv, adminSrv)
}

// serve starts both servers and blocks until ctx cancels or one of them errs,
// then drains both concurrently within shutdownTimeout. Split from run so the
// lifecycle is unit-testable without needing /data, env vars, or real ports.
func serve(ctx context.Context, storageSrv, adminSrv *http.Server) error {
	// Buffered so both ListenAndServe goroutines can report an error without
	// blocking on a receiver that may already be handling shutdown.
	errs := make(chan error, 2)
	startListener(storageSrv, "Storage server listening on port 9000", errs)
	startListener(adminSrv, "Admin server listening on port 9001", errs)

	// Emitted only after both ListenAndServe goroutines have been scheduled;
	// the E2E testcontainer waits on this exact string.
	log.Println("Server started successfully")

	var serveErr error
	select {
	case <-ctx.Done():
		log.Printf("shutdown requested, draining connections (timeout: %s)", shutdownTimeout)
	case serveErr = <-errs:
		log.Printf("server error, draining connections (timeout: %s)", shutdownTimeout)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := shutdownAll(shutdownCtx, storageSrv, adminSrv); err != nil && serveErr == nil {
		serveErr = err
	}
	if serveErr != nil {
		return serveErr
	}
	log.Println("shutdown complete")
	return nil
}

// startListener runs ListenAndServe in a goroutine, forwarding real errors to
// errs while swallowing the expected ErrServerClosed from a clean Shutdown.
func startListener(s *http.Server, startMsg string, errs chan<- error) {
	go func() {
		log.Println(startMsg)
		if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()
}

// shutdownAll drains every server concurrently under a shared deadline so one
// slow drainer does not extend the effective window for the others.
func shutdownAll(ctx context.Context, servers ...*http.Server) error {
	var wg sync.WaitGroup
	errs := make(chan error, len(servers))
	for _, s := range servers {
		wg.Add(1)
		go func(s *http.Server) {
			defer wg.Done()
			if err := s.Shutdown(ctx); err != nil {
				errs <- err
			}
		}(s)
	}
	wg.Wait()
	close(errs)

	var first error
	for err := range errs {
		if first == nil {
			first = err
			continue
		}
		log.Printf("additional shutdown error: %v", err)
	}
	return first
}

// loadEncryptionKey resolves ENCRYPTION_KEY from the environment. Raw 32-byte
// keys are accepted unchanged; anything else is decoded as base64 so operators
// can supply keys from secret stores that only emit printable text.
func loadEncryptionKey() ([]byte, error) {
	s := os.Getenv("ENCRYPTION_KEY")
	if s == "" {
		return nil, errors.New("ENCRYPTION_KEY must be provided")
	}
	if len(s) == 32 {
		return []byte(s), nil
	}
	key, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("ENCRYPTION_KEY must be 32 bytes after decoding")
	}
	return key, nil
}

// bootstrapSuperUser creates the initial admin from env vars when the user DB
// is empty. Once populated, env credentials are ignored so rotation happens
// through the admin API rather than silent restart-time overrides.
func bootstrapSuperUser() error {
	exist, err := storage.UsersExist()
	if err != nil {
		return err
	}
	if exist {
		log.Println("User database already initialized; environment credentials discarded")
		return nil
	}
	accessKey := os.Getenv("ACCESS_KEY_ID")
	secret := os.Getenv("SECRET_ACCESS_KEY")
	if accessKey == "" || secret == "" {
		return errors.New("no users in DB and ACCESS_KEY_ID/SECRET_ACCESS_KEY not provided")
	}
	if err := storage.CreateSuperUser(accessKey, secret); err != nil {
		return err
	}
	log.Println("Super user created from environment variables")
	return nil
}

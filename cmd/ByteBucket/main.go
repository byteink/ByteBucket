package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"ByteBucket/internal/handlers"
	"ByteBucket/internal/middleware"
	"ByteBucket/internal/router"
	"ByteBucket/internal/sampler"
	"ByteBucket/internal/storage"
)

// shutdownTimeout bounds how long in-flight requests get to drain before the
// process exits. 30s mirrors common orchestrator grace periods (k8s default is
// 30s terminationGracePeriodSeconds) so Shutdown will naturally lose the race
// to SIGKILL past this point — keeping it here means any leaked connection is
// visible as a shutdown error rather than a silent kill.
const shutdownTimeout = 30 * time.Second

// requestSampleInterval is how often the request-outcome sampler snapshots the
// counters. One minute matches the chart's finest bucket (the 1h range), so no
// finer resolution would ever be displayed.
const requestSampleInterval = time.Minute

// Access-log flush cadence. A data event becomes durable within at most
// eventFlushInterval, or sooner once a batch reaches eventFlushMaxBatch. The
// pair bounds both write amplification (one fsync per batch) and worst-case
// visibility latency, keeping the firehose off the request path.
const (
	eventFlushInterval = 250 * time.Millisecond
	eventFlushMaxBatch = 500
)

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

// Per-surface request body caps. Enforced by middleware.BodyLimit which emits
// a protocol-correct 413 EntityTooLarge before the handler can stream the
// body to disk.
//
//   - storageBodyLimit: 5 GiB matches the single-object PUT ceiling AWS S3
//     itself imposes. Clients uploading larger objects must use multipart
//     (planned); that path will set its own per-part limit.
//   - adminBodyLimit: 100 MiB is orders of magnitude above any legitimate
//     CORS config or user-management payload and exists purely as a
//     defence-in-depth bound for the admin surface.
const (
	storageBodyLimit int64 = 5 << 30
	adminBodyLimit   int64 = 100 << 20
)

// withBodyLimit wraps an http.Handler (typically a *gin.Engine) so any request
// body larger than limit produces a protocol-correct 413 before the handler
// runs. Applied at the cmd layer because gin.Engine.Use only applies to
// routes registered after the call; wrapping at the net/http layer sidesteps
// that ordering hazard and keeps router constructors surface-agnostic.
func withBodyLimit(h http.Handler, limit int64) http.Handler {
	return middleware.BodyLimit(h, limit)
}

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
			slog.Info("Creating missing directory", "dir", dir)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return err
			}
		}
	}
	slog.Info("Required directories are present")
	return nil
}

// Default loopback endpoints the in-container Docker HEALTHCHECK probes. They
// live as a package-level slice so tests can swap them for an httptest server
// without exposing a CLI flag — surfacing the URL would invite operators to
// point the probe at the wrong host and silently mask real failures.
var healthCheckURLs = []string{
	"http://127.0.0.1:9000/health",
	"http://127.0.0.1:9001/health",
}

// healthCheckTimeout bounds each probe. Kept tight because Docker invokes the
// probe on a 30s interval; any value above ~5s starts to delay the unhealthy
// transition past one full interval and defeats the point of the check.
const healthCheckTimeout = 3 * time.Second

// runHealthCheck probes every URL in sequence and returns the first failure.
// Sequential (not parallel) because the probe runs every 30s, the checks are
// loopback-cheap, and parallel errors would obscure which surface broke.
func runHealthCheck(urls []string) error {
	client := &http.Client{Timeout: healthCheckTimeout}
	for _, url := range urls {
		if err := probeHealth(client, url); err != nil {
			return err
		}
	}
	return nil
}

// probeHealth issues a single GET and treats anything other than a 200 as a
// failure. The body is drained-and-discarded so the connection can be reused
// by a future probe even though we never keep it warm in practice.
func probeHealth(client *http.Client, url string) error {
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("%s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: status %d", url, resp.StatusCode)
	}
	return nil
}

func main() {
	// Healthcheck is routed before any other startup so the Docker probe stays
	// cheap and side-effect-free: no logger, no /data, no UserStore, no signal
	// handlers. Just a tiny HTTP client. Anything heavier would multiply the
	// per-interval cost and risk a probe failure on transient init state that
	// has nothing to do with whether the running servers are healthy.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := runHealthCheck(healthCheckURLs); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	// Install the structured logger before any other startup work so every
	// subsequent message — including directory creation and credential
	// bootstrapping — flows through the configured handler.
	configureLogger()

	// NotifyContext gives a single cancellable context that trips on either a
	// user-initiated Ctrl+C (SIGINT) or an orchestrator-initiated SIGTERM. The
	// returned stop releases signal handlers so a second signal aborts.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		slog.Error("server error", "err", err.Error())
		os.Exit(1)
	}
}

// configureLogger wires slog's default handler from environment variables so
// operators can tune verbosity (LOG_LEVEL) and format (LOG_FORMAT=json|text)
// without a rebuild. JSON is the default because that is what every modern
// log aggregator consumes; text exists for local dev readability.
func configureLogger() {
	level := parseLogLevel(os.Getenv("LOG_LEVEL"))
	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	switch strings.ToLower(os.Getenv("LOG_FORMAT")) {
	case "text":
		handler = slog.NewTextHandler(os.Stderr, opts)
	default:
		handler = slog.NewJSONHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(handler))
}

// parseLogLevel resolves the LOG_LEVEL env var. Unknown or empty values fall
// back to INFO — surfacing a startup error for a malformed knob would be
// worse than silently defaulting to the safe verbosity.
func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
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

	// The unified event log (control-plane audit + data-plane access) lives in
	// its own file so the access firehose never threatens the auth store.
	if err := storage.InitEventStore("/data/logs.db"); err != nil {
		return err
	}

	if err := bootstrapSuperUser(); err != nil {
		return err
	}

	// PUBLIC_BASE_URL is the public origin under which the S3 storage surface
	// is reachable (e.g. https://bb.example.com). It anchors both the admin
	// UI's shareable links and server-minted presigned URLs. Unset, it
	// defaults to the local storage port so presign and public links work out
	// of the box on localhost; remapped ports or a TLS-terminating proxy need
	// an explicit value.
	handlers.SetPublicBaseURL(resolvePublicBaseURL())

	// Object-write durability defaults ON: a PUT/Copy is fsync'd before the
	// response returns. SYNC_WRITES=false trades that for throughput. A persisted
	// runtime override (admin settings UI) wins and survives restarts.
	handlers.SetSyncWrites(parseBoolEnvDefault("SYNC_WRITES", true))
	syncEff, err := handlers.InitSyncWritesFromStore()
	if err != nil {
		return err
	}
	slog.Info("object write durability", "fsync", syncEff)

	// Request-outcome time series: a background sampler snapshots the cumulative
	// S3 request counters once a minute into BoltDB, pruning past the retention
	// window, so the dashboard can chart 2xx/4xx/5xx over a navigable range. The
	// goroutine stops when ctx cancels; losing the final partial minute on
	// shutdown is acceptable for a trend series.
	retDays, err := handlers.InitRequestRetentionFromStore()
	if err != nil {
		return err
	}
	slog.Info("request-sample retention", "days", retDays)
	go sampler.Run(ctx, requestSampleInterval, middleware.S3RequestOutcomes, handlers.RequestRetentionDays)

	// Data-plane access log: OFF by default. ACCESS_LOG_ENABLED seeds the master
	// switch; MAX_EVENTS/MAX_AGE_DAYS seed the retention caps over the built-in
	// defaults. A persisted runtime override (admin settings UI) wins and
	// survives restarts. The flusher drains the buffered events into logs.db in
	// batches off the request path; it stops when ctx cancels, draining the last
	// partial batch first.
	storage.SetAccessLogEnabled(parseBoolEnv("ACCESS_LOG_ENABLED"))
	storage.SetAccessLogMaxEvents(parseIntEnvDefault("ACCESS_LOG_MAX_EVENTS", storage.AccessLogMaxEvents()))
	storage.SetAccessLogMaxAge(time.Duration(parseIntEnvDefault("ACCESS_LOG_MAX_AGE_DAYS",
		int(storage.AccessLogMaxAge()/(24*time.Hour)))) * 24 * time.Hour)
	accessCfg, err := handlers.InitAccessLogFromStore()
	if err != nil {
		return err
	}
	slog.Info("data-plane access log", "enabled", accessCfg.Enabled,
		"max_events", accessCfg.MaxEvents, "max_age_days", accessCfg.MaxAgeDays)
	go storage.RunEventFlusher(ctx, eventFlushInterval, eventFlushMaxBatch)

	// Trusted-proxy client-IP resolution is server-wide infra shared by the rate
	// limiter, access log and request log, so they all agree on who the client
	// is. Seed the env baseline, then let any persisted admin override win (it
	// survives restarts). Empty by default: trust no header, key on the peer.
	storage.SetTrustedProxy(loadTrustedProxyConfig())
	tpCfg, err := handlers.InitTrustedProxyFromStore()
	if err != nil {
		return err
	}
	slog.Info("trusted proxy", "headers", tpCfg.Headers, "use_leftmost_ip", tpCfg.UseLeftmostIP)

	// Request rate limiting is opt-in and OFF by default; the environment
	// seeds a baseline (see loadRateLimitConfig for the env contract). The
	// controller is shared by both surfaces and is always installed — disabled
	// just means it short-circuits — so an admin can enable or retune it at
	// runtime via the admin API without a restart.
	rlCfg := loadRateLimitConfig()
	rlCtrl := middleware.NewRateLimitController(rlCfg)
	handlers.SetRateLimitController(rlCtrl, rlCfg)
	// A persisted runtime override wins over the environment baseline and
	// survives restarts; apply it before the servers accept traffic.
	eff, err := handlers.InitRateLimitFromStore()
	if err != nil {
		return err
	}
	if eff.Enabled {
		slog.Info("request rate limiting enabled",
			"rps", eff.RPS, "burst", eff.Burst)
	}

	storageSrv := newServer(":9000", withBodyLimit(router.NewStorageRouter(rlCtrl), storageBodyLimit))
	adminSrv := newServer(":9001", withBodyLimit(router.NewAdminRouter(rlCtrl), adminBodyLimit))
	return serve(ctx, storageSrv, adminSrv)
}

// loadRateLimitConfig resolves the RATE_LIMIT_* environment variables. It
// mirrors the existing env-loading style (read string, parse, default on
// malformed input) so operators tune limiting without a rebuild. Defaults
// keep the feature off and harmless:
//
//   - RATE_LIMIT_ENABLED  (bool, default false) master switch.
//   - RATE_LIMIT_RPS      (float, default 0) sustained requests/sec per client.
//   - RATE_LIMIT_BURST    (int, default 0) token-bucket depth.
//   - RATE_LIMIT_TRUSTED_PROXIES (int, default 0) reverse-proxy hops in front
//     of this server, used to pick the real client IP out of X-Forwarded-For.
//
// A malformed numeric value is logged and treated as its zero default rather
// than aborting startup: a rate-limit knob is not worth taking the process
// down over, and the middleware clamps non-positive RPS/burst to safe floors.
func loadRateLimitConfig() middleware.RateLimitConfig {
	cfg := middleware.RateLimitConfig{
		Enabled: parseBoolEnv("RATE_LIMIT_ENABLED"),
		RPS:     parseFloatEnv("RATE_LIMIT_RPS"),
		Burst:   parseIntEnv("RATE_LIMIT_BURST"),
	}
	return cfg
}

// loadTrustedProxyConfig resolves the trusted-proxy environment baseline used to
// extract the real client IP behind a reverse proxy. The admin UI may override
// it at runtime (the override persists and wins). Empty headers means trust no
// header — the socket peer is the client, the safe default for a server exposed
// directly on the network.
//
//   - TRUSTED_PROXY_HEADERS (CSV, default empty) ordered header names trusted to
//     carry the client IP, e.g. "CF-Connecting-IP,X-Forwarded-For". The first
//     present header wins; for a multi-value header the rightmost entry is used.
//   - TRUSTED_PROXY_USE_LEFTMOST_IP (bool, default false) read the leftmost
//     rather than the safer rightmost entry of a multi-value header.
//
// Back-compat: the former RATE_LIMIT_TRUSTED_PROXIES integer is gone. A
// deployment that set it (>0) sits behind a proxy and previously had its
// X-Forwarded-For trusted, so seed that when no new header is configured — an
// upgrade must not silently start logging and rate-limiting the proxy's address.
func loadTrustedProxyConfig() storage.TrustedProxyConfig {
	cfg := storage.TrustedProxyConfig{
		Headers:       parseCSVEnv("TRUSTED_PROXY_HEADERS"),
		UseLeftmostIP: parseBoolEnv("TRUSTED_PROXY_USE_LEFTMOST_IP"),
	}
	if len(cfg.Headers) == 0 && parseIntEnv("RATE_LIMIT_TRUSTED_PROXIES") > 0 {
		cfg.Headers = []string{"X-Forwarded-For"}
	}
	return cfg
}

// parseCSVEnv splits a comma-separated env var into trimmed, non-empty entries,
// preserving order. Empty or unset yields an empty slice.
func parseCSVEnv(key string) []string {
	raw := strings.Split(os.Getenv(key), ",")
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// parseBoolEnv reads a boolean env var. Empty or malformed values are false,
// matching the deny-by-default posture for an opt-in feature.
func parseBoolEnv(key string) bool {
	v, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(key)))
	if err != nil {
		return false
	}
	return v
}

// defaultPublicBaseURL is the origin presigned URLs and public links use when
// PUBLIC_BASE_URL is unset. It points at the storage surface's default port so
// a localhost deployment works without configuration.
const defaultPublicBaseURL = "http://localhost:9000"

// resolvePublicBaseURL returns the configured PUBLIC_BASE_URL, or the localhost
// storage default when unset, so presign and public links never hard-fail on
// an unconfigured local run.
func resolvePublicBaseURL() string {
	if v := strings.TrimSpace(os.Getenv("PUBLIC_BASE_URL")); v != "" {
		return v
	}
	return defaultPublicBaseURL
}

// parseBoolEnvDefault reads a boolean env var, returning def when the value is
// empty or malformed. Used for opt-OUT flags (default on) where absence must
// not read as false, unlike parseBoolEnv's deny-by-default opt-IN semantics.
func parseBoolEnvDefault(key string, def bool) bool {
	s := strings.TrimSpace(os.Getenv(key))
	if s == "" {
		return def
	}
	v, err := strconv.ParseBool(s)
	if err != nil {
		return def
	}
	return v
}

// malformedEnvMsg is logged when a numeric env var fails to parse, so a typo is
// visible rather than silently swallowed.
const malformedEnvMsg = "ignoring malformed env value"

// parseIntEnvDefault reads an integer env var, returning def when the value is
// empty or malformed. Used for tunables whose absence must keep the built-in
// default rather than collapse to zero, unlike parseIntEnv's deny-by-default 0.
func parseIntEnvDefault(key string, def int) int {
	s := strings.TrimSpace(os.Getenv(key))
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		slog.Warn(malformedEnvMsg, "key", key, "value", s)
		return def
	}
	return v
}

// parseFloatEnv reads a float env var, returning 0 on empty/malformed input
// and logging the bad value so a typo is visible rather than silent.
func parseFloatEnv(key string) float64 {
	s := strings.TrimSpace(os.Getenv(key))
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		slog.Warn(malformedEnvMsg, "key", key, "value", s)
		return 0
	}
	return v
}

// parseIntEnv reads an integer env var, returning 0 on empty/malformed input
// and logging the bad value so a typo is visible rather than silent.
func parseIntEnv(key string) int {
	s := strings.TrimSpace(os.Getenv(key))
	if s == "" {
		return 0
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		slog.Warn(malformedEnvMsg, "key", key, "value", s)
		return 0
	}
	return v
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
	slog.Info("Server started successfully")

	var serveErr error
	select {
	case <-ctx.Done():
		slog.Info("shutdown requested, draining connections", "timeout", shutdownTimeout.String())
	case serveErr = <-errs:
		slog.Info("server error, draining connections", "timeout", shutdownTimeout.String())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := shutdownAll(shutdownCtx, storageSrv, adminSrv); err != nil && serveErr == nil {
		serveErr = err
	}
	if serveErr != nil {
		return serveErr
	}
	slog.Info("shutdown complete")
	return nil
}

// startListener runs ListenAndServe in a goroutine, forwarding real errors to
// errs while swallowing the expected ErrServerClosed from a clean Shutdown.
func startListener(s *http.Server, startMsg string, errs chan<- error) {
	go func() {
		slog.Info(startMsg)
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
		slog.Error("additional shutdown error", "err", err.Error())
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
		slog.Info("User database already initialized; environment credentials discarded")
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
	slog.Info("Super user created from environment variables")
	return nil
}

package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// loopbackEphemeral binds the loopback interface to a kernel-chosen port so
// tests never collide with a real 9000/9001 server or each other.
const loopbackEphemeral = "127.0.0.1:0"

// reserveAddr asks the kernel for a free port, then closes the listener so
// http.Server can re-bind it. Races between close and re-bind are possible in
// principle but have never materialised on Darwin/Linux in practice.
func reserveAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", loopbackEphemeral)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// TestServeShutsDownOnContextCancel exercises the production shutdown path:
// both servers serve real traffic, the context cancels, and serve must return
// nil within the shutdown budget.
func TestServeShutsDownOnContextCancel(t *testing.T) {
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	s1 := &http.Server{Addr: reserveAddr(t), Handler: okHandler}
	s2 := &http.Server{Addr: reserveAddr(t), Handler: okHandler}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, s1, s2) }()

	// Poll until both servers answer, then cancel. Without this the test could
	// race ahead and cancel before ListenAndServe has bound its socket.
	waitForHTTP(t, "http://"+s1.Addr)
	waitForHTTP(t, "http://"+s2.Addr)

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned error on clean shutdown: %v", err)
		}
	case <-time.After(shutdownTimeout + 2*time.Second):
		t.Fatal("serve did not return within shutdown budget")
	}
}

// TestServeShutsDownOnAlreadyCanceledContext guards the fast path: a context
// that is already canceled must still produce a clean shutdown, not a panic
// or a hang, so operators can rely on Shutdown under any invocation order.
func TestServeShutsDownOnAlreadyCanceledContext(t *testing.T) {
	s1 := &http.Server{Addr: reserveAddr(t), Handler: http.NewServeMux()}
	s2 := &http.Server{Addr: reserveAddr(t), Handler: http.NewServeMux()}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() { done <- serve(ctx, s1, s2) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned error: %v", err)
		}
	case <-time.After(shutdownTimeout + 2*time.Second):
		t.Fatal("serve did not return within shutdown budget")
	}
}

// waitForHTTP polls url until it answers with any status or the deadline hits.
func waitForHTTP(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("server at %s never became ready", url)
}

// TestRunHealthCheckPassesOnAllOK locks the happy path: every probe sees a 200
// so the function must return nil. Two servers (not one) so the loop body is
// actually exercised - a regression that returns after the first probe would
// pass a single-URL test.
func TestRunHealthCheckPassesOnAllOK(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	a := httptest.NewServer(ok)
	defer a.Close()
	b := httptest.NewServer(ok)
	defer b.Close()

	if err := runHealthCheck([]string{a.URL + "/health", b.URL + "/health"}); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

// TestRunHealthCheckFailsOnNon200 guards against a server that answers but is
// not actually serving traffic (e.g. 503 during a drain). Anything other than
// 200 must surface as a failure so the orchestrator restarts the container.
func TestRunHealthCheckFailsOnNon200(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer s.Close()

	err := runHealthCheck([]string{s.URL + "/health"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Fatalf("expected status code in error, got %v", err)
	}
}

// TestRunHealthCheckFailsOnUnreachable covers the case where the server is not
// listening at all - a closed port refuses immediately and must produce an
// error rather than silently passing.
func TestRunHealthCheckFailsOnUnreachable(t *testing.T) {
	// Reserve and immediately release a port so the address is well-formed but
	// nothing is listening. Beats a hard-coded port that might collide with a
	// real service on the developer's machine.
	addr := reserveAddr(t)

	err := runHealthCheck([]string{"http://" + addr + "/health"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// TestRunHealthCheckShortCircuitsOnFirstFailure asserts that a failing probe
// stops the loop. Without this, a slow second probe could mask the first
// failure's latency in the unhealthy transition path.
func TestRunHealthCheckShortCircuitsOnFirstFailure(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()

	var secondCalled bool
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer good.Close()

	if err := runHealthCheck([]string{bad.URL + "/health", good.URL + "/health"}); err == nil {
		t.Fatal("expected error, got nil")
	}
	if secondCalled {
		t.Fatal("second probe should not have been called after first failure")
	}
}

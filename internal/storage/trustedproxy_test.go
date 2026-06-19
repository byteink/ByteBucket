package storage

import "testing"

// TestTrustedProxyDefaultEmpty pins the safe default: with nothing configured,
// no request header is trusted, so IP resolution falls back to the socket peer.
func TestTrustedProxyDefaultEmpty(t *testing.T) {
	trustedProxy.Store(nil)
	t.Cleanup(func() { trustedProxy.Store(nil) })
	got := TrustedProxy()
	if len(got.Headers) != 0 || got.UseLeftmostIP {
		t.Fatalf("default TrustedProxy = %+v, want empty", got)
	}
}

// TestTrustedProxySetGetCopies proves the live value is isolated from caller
// slices in both directions, so a request-path read can never race a mutation
// of the slice an admin update passed in or the slice a prior read returned.
func TestTrustedProxySetGetCopies(t *testing.T) {
	t.Cleanup(func() { trustedProxy.Store(nil) })
	in := TrustedProxyConfig{Headers: []string{"CF-Connecting-IP"}, UseLeftmostIP: true}
	SetTrustedProxy(in)

	in.Headers[0] = "X-Evil" // mutate the input after storing
	got := TrustedProxy()
	if len(got.Headers) != 1 || got.Headers[0] != "CF-Connecting-IP" || !got.UseLeftmostIP {
		t.Fatalf("TrustedProxy = %+v, want isolated copy of CF-Connecting-IP", got)
	}

	got.Headers[0] = "X-Evil2" // mutate the returned slice
	if again := TrustedProxy(); again.Headers[0] != "CF-Connecting-IP" {
		t.Fatalf("returned slice aliases stored value: %+v", again)
	}
}

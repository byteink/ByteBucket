package main

import (
	"net/http"
	"testing"
)

// TestNewServerAppliesTimeouts pins every bound that newServer must set.
// A zero value on any of these in production silently disables that protection
// (Go interprets zero Duration as "no deadline"), so regressing to a bare
// &http.Server{Handler: r} literal must fail the build, not just go unnoticed
// until a slowloris client shows up.
func TestNewServerAppliesTimeouts(t *testing.T) {
	s := newServer(":0", http.NewServeMux())

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"ReadHeaderTimeout", s.ReadHeaderTimeout, readHeaderTimeout},
		{"ReadTimeout", s.ReadTimeout, readTimeout},
		{"WriteTimeout", s.WriteTimeout, writeTimeout},
		{"IdleTimeout", s.IdleTimeout, idleTimeout},
		{"MaxHeaderBytes", s.MaxHeaderBytes, maxHeaderBytes},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

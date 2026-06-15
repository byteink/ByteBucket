// Package webui serves the embedded admin SPA.
//
// The SPA is produced by `web/` (Vite) and copied into ./dist/ as part of
// the build (Makefile / Dockerfile). dist/ is git-ignored; a .keep file
// anchors the directory so //go:embed compiles from a fresh clone. When
// dist/ is empty (no `make ui` run yet) the handler serves an inline
// fallback page so the binary is never broken.
package webui

import (
	"bytes"
	"embed"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

const (
	msgInternal   = "internal error"
	indexFileName = "index.html"
	indexPath     = "dist/" + indexFileName
)

//go:embed all:dist
var distFS embed.FS

// buildTime is used as the modtime for the embedded index.html so that
// conditional-GET handling stays stable across restarts.
var buildTime = time.Now()

// Handler returns an http.Handler that serves the embedded SPA.
// Paths that resolve to a real file under dist/ are served directly;
// unknown paths fall back to index.html so client-side routing works.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// Unreachable: embedding would have failed at compile time.
		panic(err)
	}
	return &spaHandler{root: sub}
}

type spaHandler struct {
	root fs.FS
}

// FaviconICO returns the embedded favicon.ico, or false when the UI bundle has
// not been built (dist holds only .keep). Lets the storage surface serve the
// same icon as the admin SPA without pulling in the SPA fallback handler.
func FaviconICO() ([]byte, bool) {
	b, err := distFS.ReadFile("dist/favicon.ico")
	if err != nil {
		return nil, false
	}
	return b, true
}

func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Defence-in-depth headers for every SPA response:
	//   X-Frame-Options:DENY  — the admin UI must never be framed, so a
	//     clickjacked page cannot trick an admin into invoking a button.
	//   X-Content-Type-Options:nosniff — pair with the explicit
	//     Content-Type below so browsers do not second-guess our typing.
	//   Referrer-Policy:no-referrer — admin URLs never leak via outbound
	//     links (e.g. the user clicks an external help link from the UI).
	//   Content-Security-Policy — restrict to same-origin assets, no
	//     inline scripts beyond what Vite already inlines (sha-pinned by
	//     'self' here keeps the door closed for stored-XSS routed through
	//     anywhere on the admin origin).
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; "+
			"script-src 'self'; connect-src 'self'; font-src 'self' data:; frame-ancestors 'none'; base-uri 'self'")

	clean := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
	if clean == "/" {
		h.serveIndex(w, r)
		return
	}

	name := strings.TrimPrefix(clean, "/")
	if name == "" || strings.Contains(name, "..") {
		h.serveIndex(w, r)
		return
	}

	f, err := h.root.Open(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			h.serveIndex(w, r)
			return
		}
		http.Error(w, msgInternal, http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		http.Error(w, msgInternal, http.StatusInternalServerError)
		return
	}
	if info.IsDir() {
		h.serveIndex(w, r)
		return
	}

	data, err := io.ReadAll(f)
	if err != nil {
		http.Error(w, msgInternal, http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), bytes.NewReader(data))
}

const fallbackIndex = `<!doctype html>
<html><head><meta charset="utf-8"><title>ByteBucket</title></head>
<body><p>Admin UI not built. Run <code>make ui</code> (local) or rebuild the Docker image (production).</p></body></html>
`

func (h *spaHandler) serveIndex(w http.ResponseWriter, r *http.Request) {
	data, err := distFS.ReadFile(indexPath)
	if err != nil {
		// dist/ is empty (no make ui yet). Serve an inline hint instead of
		// 500ing — the admin API on this port is still usable via curl.
		data = []byte(fallbackIndex)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, indexFileName, buildTime, bytes.NewReader(data))
}

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

func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

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

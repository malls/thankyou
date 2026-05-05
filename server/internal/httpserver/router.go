package httpserver

import (
	"io/fs"
	"net/http"
)

// NewRouter returns a configured *http.ServeMux wired with all routes.
// Static-site files (index.html, style.css, etc.) are served from the
// embedded FS at the root; API routes are prefix-routed under /api/.
//
// We use the stdlib mux instead of chi/gorilla/etc — three routes don't
// justify the dependency, and Go 1.22+'s ServeMux supports method patterns
// well enough for this surface.
func NewRouter(h *Handlers) (http.Handler, error) {
	staticFS, err := StaticFS()
	if err != nil {
		return nil, err
	}
	staticHandler := http.FileServer(http.FS(staticFS))

	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", h.HealthZ)
	mux.HandleFunc("POST /api/render", h.Render)
	mux.HandleFunc("GET /api/files/", h.File)
	mux.HandleFunc("HEAD /api/files/", h.File)

	// The fall-through route serves the static site. Anything that didn't
	// match an /api/ route or /healthz lands here. http.FileServer handles
	// the "/" -> "index.html" redirect and 404s for missing files.
	mux.Handle("/", staticHandler)

	// Verify embed is non-empty so a misconfigured build (forgot to run
	// copy-static.sh) fails loudly rather than serving 404s for everything.
	entries, err := fs.ReadDir(staticFS, ".")
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, errEmbedEmpty
	}

	return mux, nil
}

// errEmbedEmpty signals that the static FS embedded into the binary has zero
// files — almost certainly because copy-static.sh wasn't run before `go build`.
var errEmbedEmpty = &embedError{}

type embedError struct{}

func (e *embedError) Error() string {
	return "httpserver: embedded static FS is empty (did you run tools/copy-static.sh?)"
}

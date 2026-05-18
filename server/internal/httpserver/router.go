package httpserver

import (
	"io/fs"
	"net/http"

	"github.com/forrestalmasi/thankyou/server/public"
)

// NewRouter returns a configured *http.ServeMux wired with all routes.
// Static-site files (index.html, style.css, etc.) are served from the
// embedded FS at the root; API routes are prefix-routed under /api/.
//
// We use the stdlib mux instead of chi/gorilla/etc — three routes don't
// justify the dependency, and Go 1.22+'s ServeMux supports method patterns
// well enough for this surface.
func NewRouter(h *Handlers) (http.Handler, error) {
	publicFS := public.FS()
	staticHandler := http.FileServer(http.FS(publicFS))

	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", h.HealthZ)
	mux.HandleFunc("POST /api/render", h.Render)
	mux.HandleFunc("GET /api/files/", h.File)
	mux.HandleFunc("HEAD /api/files/", h.File)

	// Printful integration. The handlers themselves return 503 with file_id
	// + file_url when h.Printful == nil; the routes are always wired so the
	// 503 is observable rather than a 404.
	mux.HandleFunc("POST /api/printful/products", h.CreateTShirt)
	mux.HandleFunc("GET /api/printful/mockup/", h.MockupStatus)
	mux.HandleFunc("POST /api/printful/mockup", h.CreateMockupOnly)

	// Stripe Checkout integration. Same 503-when-unconfigured pattern as
	// Printful: routes are always wired so a misconfigured deploy gets a
	// typed JSON error rather than a 404. /api/checkout/start orchestrates
	// render → Printful sync_product → Stripe Session in one POST;
	// /api/stripe/webhook receives the signed checkout.session.completed
	// event and places the Printful order with confirm=true.
	mux.HandleFunc("POST /api/checkout/start", h.StartCheckout)
	mux.HandleFunc("POST /api/stripe/webhook", h.StripeWebhook)

	// Extension-less /checkout maps to checkout.html. The static FileServer
	// would happily serve /checkout.html, but humans type /checkout and we
	// want the cleaner URL. http.ServeFileFS (Go 1.22+) handles MIME +
	// caching headers identically to FileServer. Must be registered before
	// the "/" fall-through so the mux dispatches it explicitly.
	mux.HandleFunc("GET /checkout", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, publicFS, "checkout.html")
	})

	// The fall-through route serves the static site. Anything that didn't
	// match an /api/ route or /healthz lands here. http.FileServer handles
	// the "/" -> "index.html" redirect and 404s for missing files.
	mux.Handle("/", staticHandler)

	// Verify embed is non-empty so a corrupted build (somehow stripped of
	// its baked-in assets) fails loudly rather than serving 404s for
	// everything.
	entries, err := fs.ReadDir(publicFS, ".")
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, errEmbedEmpty
	}

	return mux, nil
}

// errEmbedEmpty signals that the public FS embedded into the binary has zero
// files — should be impossible in a clean build; indicates a corrupted binary.
var errEmbedEmpty = &embedError{}

type embedError struct{}

func (e *embedError) Error() string {
	return "httpserver: embedded public FS is empty (corrupted build?)"
}

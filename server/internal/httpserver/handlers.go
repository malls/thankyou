// Package httpserver wires the HTTP surface for the Thank You server.
// It does not own business logic — render and file storage live in their
// own packages — but it does own request validation, JSON shaping, and
// cache headers.
package httpserver

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/forrestalmasi/thankyou/server/internal/files"
	"github.com/forrestalmasi/thankyou/server/internal/render"
)

// renderRequest is the JSON body the client sends. Field names mirror the
// existing client query-string params (`text`, `middletext`) for symmetry.
type renderRequest struct {
	Text       string `json:"text"`
	MiddleText string `json:"middletext"`
	Background string `json:"background,omitempty"`
}

// renderResponse is the JSON body returned on success. file_id is the SHA-256
// hex digest; url is a relative path the client can GET to fetch the PNG.
type renderResponse struct {
	FileID string `json:"file_id"`
	URL    string `json:"url"`
}

// errorResponse is the JSON body returned on 4xx and 5xx. Stays small on
// purpose so client code can branch on `error` without parsing prose.
type errorResponse struct {
	Error   string `json:"error"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message,omitempty"`
}

// Handlers holds the dependencies needed by the HTTP layer. Constructed
// once in main.go and shared across every request.
//
// Printful and Stripe are independently optional: nil means "the relevant
// env vars were unset at boot, so the corresponding routes return 503 with
// a typed error code." See printful_handlers.go and checkout_handlers.go.
type Handlers struct {
	Renderer *render.Renderer
	Store    *files.Store
	Logger   *log.Logger
	Printful *PrintfulSetup
	Stripe   *StripeSetup
}

// MaxRenderBodyBytes caps the size of a /api/render request body. The legit
// payload is < 200 bytes; anything larger is either malicious or a bug.
const MaxRenderBodyBytes = 4 << 10 // 4 KiB

// HealthZ returns 200 OK with body "ok". Standard liveness check.
func (h *Handlers) HealthZ(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok")
}

// Render handles POST /api/render. Validates inputs, computes the content
// hash, runs the render+save through singleflight (so concurrent identical
// requests share a single render), and returns the JSON {file_id, url}.
func (h *Handlers) Render(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "", "POST required")
		return
	}

	// Cap body size so a hostile client can't make us allocate 1 GiB while
	// we wait for the JSON parser to give up.
	r.Body = http.MaxBytesReader(w, r.Body, MaxRenderBodyBytes)
	defer r.Body.Close()

	var req renderRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "", err.Error())
		return
	}

	inputs, err := render.Validate(req.Text, req.MiddleText, req.Background)
	if err != nil {
		var ve *render.ValidationError
		if errors.As(err, &ve) {
			writeError(w, http.StatusBadRequest, "validation_failed", ve.Field, ve.Reason)
			return
		}
		writeError(w, http.StatusBadRequest, "validation_failed", "", err.Error())
		return
	}

	hash := render.Hash(inputs)

	// Singleflight + atomic save: two concurrent identical POSTs produce
	// one render call and one disk write. Both responses get the same
	// file_id and a successful URL.
	_, err = h.Store.SaveDedup(hash, func() ([]byte, error) {
		return h.Renderer.RenderPNG(inputs)
	})
	if err != nil {
		h.logf("render: hash=%s err=%v", hash, err)
		writeError(w, http.StatusInternalServerError, "render_failed", "", "render failed")
		return
	}

	resp := renderResponse{
		FileID: hash,
		URL:    "/api/files/" + hash + ".png",
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
	h.logf("render: hash=%s ok", hash)
}

// File handles GET /api/files/{hash}.png. Streams the PNG from disk with a
// long-lived immutable cache header (the URL is content-addressed, so the
// content is by definition stable). 404s for any hash that doesn't match
// the expected pattern or isn't on disk.
func (h *Handlers) File(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "", "GET required")
		return
	}

	hash, ok := parseFilePath(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "", "file not found")
		return
	}

	body, size, err := h.Store.Open(hash)
	if errors.Is(err, files.ErrNotFound) || errors.Is(err, files.ErrInvalidHash) {
		writeError(w, http.StatusNotFound, "not_found", "", "file not found")
		return
	}
	if err != nil {
		h.logf("file: hash=%s err=%v", hash, err)
		writeError(w, http.StatusInternalServerError, "io_error", "", "failed to read file")
		return
	}
	defer body.Close()

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Length", itoa(size))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, body)
	}
}

// parseFilePath extracts the hash from "/api/files/{hash}.png". Returns
// false if the URL doesn't have that exact shape — letting callers serve
// a clean 404 without constructing a Store path that could fail downstream.
func parseFilePath(p string) (string, bool) {
	const prefix = "/api/files/"
	const suffix = ".png"
	if len(p) <= len(prefix)+len(suffix) {
		return "", false
	}
	if p[:len(prefix)] != prefix {
		return "", false
	}
	if p[len(p)-len(suffix):] != suffix {
		return "", false
	}
	return p[len(prefix) : len(p)-len(suffix)], true
}

// itoa is a tiny stdlib-free Int64 -> string for Content-Length. Avoids the
// strconv import in this file's hot path; not measurably faster but tidy.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	negative := false
	if n < 0 {
		negative = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// writeError serialises a uniform JSON error envelope for 4xx/5xx responses.
// Field is optional — only validation errors set it.
func writeError(w http.ResponseWriter, status int, code, field, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{
		Error:   code,
		Field:   field,
		Message: message,
	})
}

// logf is a small wrapper that no-ops if no logger was wired in. Lets tests
// construct Handlers without bothering with log plumbing.
func (h *Handlers) logf(format string, args ...any) {
	if h.Logger == nil {
		return
	}
	h.Logger.Printf(format, args...)
}

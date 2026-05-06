package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"github.com/forrestalmasi/thankyou/server/internal/printful"
	"github.com/forrestalmasi/thankyou/server/internal/render"
)

// MaxPrintfulBodyBytes caps the request body for the Printful endpoints.
// Same threshold as MaxRenderBodyBytes — request shape is the same plus a
// few optional fields.
const MaxPrintfulBodyBytes = 4 << 10 // 4 KiB

// printfulProductRequest is the body for POST /api/printful/products. It
// extends renderRequest with an optional VariantIDs override; absent means
// "use the catalog defaults". A future variant-picker UI fills this in.
type printfulProductRequest struct {
	Text       string `json:"text"`
	MiddleText string `json:"middletext"`
	Background string `json:"background,omitempty"`
	VariantIDs []int  `json:"variant_ids,omitempty"`
}

// printfulProductResponse merges the file-store side and the Printful side
// into a single 200 body. mockup_status_url is the relative path the
// browser polls; the server hides the bearer token.
type printfulProductResponse struct {
	FileID          string `json:"file_id"`
	FileURL         string `json:"file_url"`
	SyncProductID   int64  `json:"sync_product_id,omitempty"`
	ExternalID      string `json:"external_id,omitempty"`
	MockupTaskID    int64  `json:"mockup_task_id,omitempty"`
	MockupStatusURL string `json:"mockup_status_url,omitempty"`
}

// printfulPartialResponse is the 502 body shape when one of the parallel
// Printful calls fails but the other succeeded. Lets the client decide
// which half to retry.
type printfulPartialResponse struct {
	Error      string         `json:"error"`
	Message    string         `json:"message"`
	FileID     string         `json:"file_id"`
	FileURL    string         `json:"file_url"`
	ExternalID string         `json:"external_id"`
	Partial    partialDetails `json:"partial"`
}

type partialDetails struct {
	MockupOK        bool   `json:"mockup_ok"`
	SyncProductOK   bool   `json:"sync_product_ok"`
	MockupTaskID    int64  `json:"mockup_task_id,omitempty"`
	MockupStatusURL string `json:"mockup_status_url,omitempty"`
	SyncProductID   int64  `json:"sync_product_id,omitempty"`
	MockupError     string `json:"mockup_error,omitempty"`
	SyncError       string `json:"sync_product_error,omitempty"`
}

// printfulMockupRequest is the body for POST /api/printful/mockup. Caller
// can supply either the rendered file_id (cheaper path; reuses the file)
// or text/middletext (which the server then renders+saves first).
type printfulMockupRequest struct {
	FileID     string `json:"file_id,omitempty"`
	Text       string `json:"text,omitempty"`
	MiddleText string `json:"middletext,omitempty"`
	Background string `json:"background,omitempty"`
	VariantIDs []int  `json:"variant_ids,omitempty"`
}

// printfulMockupResponse is the 200 body from the standalone mockup route.
type printfulMockupResponse struct {
	FileID    string `json:"file_id"`
	FileURL   string `json:"file_url"`
	TaskID    int64  `json:"task_id"`
	StatusURL string `json:"status_url"`
}

// taskIDPattern restricts the path parameter to a numeric id we can pass
// upstream. Anything else gets a 400 without a Printful round-trip — Printful
// expects ints and arbitrary strings would be a small attack surface.
var taskIDPattern = regexp.MustCompile(`^[0-9]{1,20}$`)

// PrintfulFanoutTimeout caps the total time the parallel fanout can take.
// The two Printful calls run concurrently inside this budget; partial
// success is preferred over a hard timeout.
const PrintfulFanoutTimeout = 25 * time.Second

// printfulHandlers extends Handlers with the Printful-specific dependencies.
// Kept on the Handlers struct (see PrintfulSetup) so the router wiring stays
// uniform across all routes.

// PrintfulSetup is the optional dependency block. Constructed in main.go
// when PRINTFUL_TOKEN is present; left nil otherwise (handlers detect the
// nil and 503 with file_id+file_url so the UI degrades gracefully).
//
// The public base URL used to build file URLs Printful fetches lives on the
// top-level Handlers struct (Handlers.PublicBaseURL), not here — it's shared
// with the Stripe success_url/cancel_url builder.
type PrintfulSetup struct {
	Client *printful.Client
	// SyncProductSF dedupes concurrent identical sync-product creates. The
	// idempotency flow is GET-then-POST, but two concurrent goroutines can
	// both see 404 and both POST; singleflight collapses them. Keyed on
	// external_id.
	SyncProductSF singleflight.Group
}

// CreateTShirt handles POST /api/printful/products. Renders+saves the print
// PNG (reusing the existing render path), then runs the parallel mockup +
// sync-product fanout against Printful.
//
// Status code conventions:
//   - 200 — full success.
//   - 400 — validation error.
//   - 502 — Printful upstream failed (full or partial); body explains.
//   - 503 — server not configured for Printful (PRINTFUL_TOKEN missing);
//     body still includes file_id/file_url so the UI can show the saved design.
func (h *Handlers) CreateTShirt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "", "POST required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxPrintfulBodyBytes)
	defer r.Body.Close()

	var req printfulProductRequest
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

	// Render+save up front so even the 503 path can hand the client a
	// usable file_id/file_url. Plan §4: 503 body must include them.
	if _, err := h.Store.SaveDedup(hash, func() ([]byte, error) {
		return h.Renderer.RenderPNG(r.Context(), inputs)
	}); err != nil {
		h.logf("printful/products: hash=%s render err=%v", hash, err)
		writeError(w, http.StatusInternalServerError, "render_failed", "", "render failed")
		return
	}
	relativeURL := "/api/files/" + hash + ".png"

	// Unconfigured: degrade gracefully. The render+save above still happened.
	if h.Printful == nil || h.Printful.Client == nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":    "printful_unconfigured",
			"message":  "server is missing PRINTFUL_TOKEN; the design was rendered and saved but Printful integration is offline",
			"file_id":  hash,
			"file_url": relativeURL,
		})
		h.logf("printful/products: hash=%s unconfigured 503", hash)
		return
	}

	publicFileURL := h.publicFileURL(hash)
	externalID := printful.ExternalIDForHash(hash)
	variantIDs := req.VariantIDs
	if len(variantIDs) == 0 {
		variantIDs = printful.DefaultVariantIDs()
	}

	ctx, cancel := context.WithTimeout(r.Context(), PrintfulFanoutTimeout)
	defer cancel()

	var (
		mockupResp  printful.CreateMockupTaskResponse
		productResp printful.SyncProductData
		mockupErr   error
		productErr  error
	)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		mockupResp, mockupErr = h.Printful.Client.CreateMockupTask(gctx, buildMockupRequest(publicFileURL, variantIDs))
		// Don't return the error to errgroup — we want both halves to run
		// to completion regardless of the other's outcome (partial-success
		// reporting). Returning nil here keeps the group from cancelling
		// the sibling goroutine.
		return nil
	})
	g.Go(func() error {
		productResp, productErr = h.createOrFetchSyncProduct(gctx, externalID, hash, publicFileURL, variantIDs)
		return nil
	})
	_ = g.Wait()

	mockupOK := mockupErr == nil
	productOK := productErr == nil

	switch {
	case mockupOK && productOK:
		resp := printfulProductResponse{
			FileID:          hash,
			FileURL:         relativeURL,
			SyncProductID:   productResp.ID,
			ExternalID:      externalID,
			MockupTaskID:    mockupResp.ID,
			MockupStatusURL: mockupStatusURL(mockupResp.ID),
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
		h.logf("printful/products: hash=%s sync_id=%d task_id=%d ok",
			hash, productResp.ID, mockupResp.ID)
		return

	default:
		// Partial or full failure. 502 with a body listing what survived.
		body := printfulPartialResponse{
			Error:      "printful_partial",
			Message:    "one or more Printful calls failed; see partial",
			FileID:     hash,
			FileURL:    relativeURL,
			ExternalID: externalID,
			Partial: partialDetails{
				MockupOK:      mockupOK,
				SyncProductOK: productOK,
			},
		}
		if mockupOK {
			body.Partial.MockupTaskID = mockupResp.ID
			body.Partial.MockupStatusURL = mockupStatusURL(mockupResp.ID)
		} else {
			body.Partial.MockupError = errMessageForClient(mockupErr)
		}
		if productOK {
			body.Partial.SyncProductID = productResp.ID
		} else {
			body.Partial.SyncError = errMessageForClient(productErr)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(body)
		h.logf("printful/products: hash=%s partial mockup_ok=%v sync_ok=%v mockup_err=%v sync_err=%v",
			hash, mockupOK, productOK, mockupErr, productErr)
	}
}

// MockupStatus handles GET /api/printful/mockup/{task_id}. Pass-through
// to GetMockupTask with the bearer token attached server-side. 401 from
// upstream becomes 502 (the inbound client isn't the one missing creds).
func (h *Handlers) MockupStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "", "GET required")
		return
	}

	const prefix = "/api/printful/mockup/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeError(w, http.StatusNotFound, "not_found", "", "")
		return
	}
	taskIDStr := r.URL.Path[len(prefix):]
	if !taskIDPattern.MatchString(taskIDStr) {
		writeError(w, http.StatusBadRequest, "invalid_task_id", "", "task_id must be 1-20 digits")
		return
	}
	taskID, err := strconv.ParseInt(taskIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_task_id", "", err.Error())
		return
	}

	if h.Printful == nil || h.Printful.Client == nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":   "printful_unconfigured",
			"message": "server is missing PRINTFUL_TOKEN",
		})
		return
	}

	resp, err := h.Printful.Client.GetMockupTask(r.Context(), taskID)
	if err != nil {
		// 401 from upstream is a server-side credential problem, not a
		// client auth failure on the inbound request. Translate to 502.
		status := http.StatusBadGateway
		writeError(w, status, "printful_upstream_error", "", errMessageForClient(err))
		h.logf("printful/mockup: task_id=%d err=%v", taskID, err)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// CreateMockupOnly handles POST /api/printful/mockup — the optional
// standalone route. Useful for dev: kick off a mockup without committing
// to a sync product. Body is either {file_id} (reuse a previous render)
// or {text, middletext} (render fresh). Returns {task_id, status_url, file_url, file_id}.
func (h *Handlers) CreateMockupOnly(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "", "POST required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxPrintfulBodyBytes)
	defer r.Body.Close()

	var req printfulMockupRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "", err.Error())
		return
	}

	var hash string
	if req.FileID != "" {
		// Reuse path: just confirm the file exists on disk.
		exists, err := h.Store.Exists(req.FileID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_file_id", "", err.Error())
			return
		}
		if !exists {
			writeError(w, http.StatusNotFound, "file_not_found", "", "file_id does not exist on disk")
			return
		}
		hash = req.FileID
	} else {
		// Render path: validate and save like /api/printful/products.
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
		hash = render.Hash(inputs)
		if _, err := h.Store.SaveDedup(hash, func() ([]byte, error) {
			return h.Renderer.RenderPNG(r.Context(), inputs)
		}); err != nil {
			h.logf("printful/mockup: hash=%s render err=%v", hash, err)
			writeError(w, http.StatusInternalServerError, "render_failed", "", "render failed")
			return
		}
	}

	relativeURL := "/api/files/" + hash + ".png"

	if h.Printful == nil || h.Printful.Client == nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":    "printful_unconfigured",
			"message":  "server is missing PRINTFUL_TOKEN; the design was rendered and saved",
			"file_id":  hash,
			"file_url": relativeURL,
		})
		return
	}

	publicFileURL := h.publicFileURL(hash)
	variantIDs := req.VariantIDs
	if len(variantIDs) == 0 {
		variantIDs = printful.DefaultVariantIDs()
	}

	resp, err := h.Printful.Client.CreateMockupTask(r.Context(), buildMockupRequest(publicFileURL, variantIDs))
	if err != nil {
		writeError(w, http.StatusBadGateway, "printful_upstream_error", "", errMessageForClient(err))
		h.logf("printful/mockup: hash=%s err=%v", hash, err)
		return
	}

	out := printfulMockupResponse{
		FileID:    hash,
		FileURL:   relativeURL,
		TaskID:    resp.ID,
		StatusURL: mockupStatusURL(resp.ID),
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(out)
	h.logf("printful/mockup: hash=%s task_id=%d ok", hash, resp.ID)
}

// createOrFetchSyncProduct delegates to printful.Client.CreateOrFetchSyncProduct
// using the per-handler singleflight so concurrent identical creates collapse
// to one upstream POST. Kept as a thin wrapper to preserve the call shape and
// hash parameter — the package helper takes externalID + fileURL + variantIDs
// (the hash is implicit in externalID via printful.ExternalIDForHash).
func (h *Handlers) createOrFetchSyncProduct(ctx context.Context, externalID, hash, fileURL string, variantIDs []int) (printful.SyncProductData, error) {
	_ = hash // signature retained for log/trace symmetry; consumed by externalID
	return h.Printful.Client.CreateOrFetchSyncProduct(ctx, &h.Printful.SyncProductSF, externalID, fileURL, variantIDs)
}

// buildMockupRequest constructs the v2 mockup-tasks body for the V1 catalog
// (Bella+Canvas 3001, front placement, dtg).
func buildMockupRequest(fileURL string, variantIDs []int) printful.CreateMockupTaskRequest {
	return printful.CreateMockupTaskRequest{
		Format: "png",
		Products: []printful.MockupProduct{{
			Source:            "catalog",
			CatalogProductID:  printful.BellaCanvas3001ProductID,
			CatalogVariantIDs: variantIDs,
			Placements: []printful.Placement{{
				Placement: printful.DefaultPrintPlacement,
				Technique: printful.DefaultPrintTechnique,
				Layers:    []printful.Layer{{Type: "file", URL: fileURL}},
			}},
		}},
	}
}

// mockupStatusURL is the relative URL the client polls. Server-relative
// (no host); the browser fills in its own origin.
func mockupStatusURL(taskID int64) string {
	return "/api/printful/mockup/" + strconv.FormatInt(taskID, 10)
}

// publicFileURL builds the absolute URL Printful will GET to fetch the print
// PNG. The base is configured at boot (PUBLIC_BASE_URL); main.go fails fast
// when that env var is empty while Printful or Stripe is configured, so we
// don't accept the empty case here. The inbound *http.Request is intentionally
// not consulted — Host is attacker-controlled in non-browser clients and a
// fallback would let an attacker pin Printful sync_product file URLs at their
// own host (durable: keyed on external_id derived from the design hash).
func (h *Handlers) publicFileURL(hash string) string {
	return strings.TrimRight(h.PublicBaseURL, "/") + "/api/files/" + hash + ".png"
}

// errMessageForClient extracts a safe human message from a Printful error
// to surface in the JSON response. Strips the bearer-token-could-be-here
// detail; the typed errors don't carry the token, but be defensive.
func errMessageForClient(err error) string {
	if err == nil {
		return ""
	}
	var apiErr *printful.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Message != "" {
			return fmt.Sprintf("printful %d: %s", apiErr.StatusCode, apiErr.Message)
		}
		return fmt.Sprintf("printful %d", apiErr.StatusCode)
	}
	// Fallback: the wrapped error from do() includes "printful: METHOD path: ..."
	// — safe to expose as it doesn't include the token.
	return err.Error()
}

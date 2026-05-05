package render

import (
	"context"
	"fmt"
	"sync"

	resvg "github.com/kanrichan/resvg-go"
)

// Output dimensions of the rasterised PNG. 3600x4800 = 12"x16" at 300 DPI,
// which matches the Bella+Canvas 3001 DTG print area Printful recommends.
// See TYB-6 plan section 3.
const (
	OutputWidth  = 3600
	OutputHeight = 4800
)

// Renderer rasterises validated Inputs into PNG bytes.
//
// We keep a single resvg.Context (the wasm runtime — ~50ms boot cost) and a
// single resvg.Renderer (which holds the font database) for the lifetime of
// the server. Renders are serialised through a mutex because the wasm
// renderer holds per-call state and is not safe for concurrent use.
//
// resvg-go v0.0.1 has a bug in (*Renderer).Close — it calls the underlying
// wasm `__renderer_delete` with no arguments even though the export expects
// a pointer. Calling Close always errors. We avoid it by:
//
//   - never closing the per-render-call Renderer (we don't allocate one);
//   - using a single long-lived Renderer instead, which gets implicitly
//     freed when the wasm Context is closed at shutdown.
//
// If throughput ever becomes a problem (it won't for this prototype — single
// user, occasional clicks) the fix is a pool of renderers behind the same
// resvg.Context, not finer-grained locking inside the wasm module.
type Renderer struct {
	mu  sync.Mutex
	ctx *resvg.Context
	r   *resvg.Renderer
}

// NewRenderer boots the wasm runtime, allocates a resvg renderer, and loads
// the embedded font into its font database. Call Close when shutting the
// server down — that frees the wasm Context (and, transitively, the
// renderer's allocations).
//
// resvg's underlying font database (fontdb) accepts SFNT font formats
// (TTF/OTF/TTC) but not WOFF/WOFF2. The repo ships a .woff so we decompress
// it to SFNT in-process at boot via woffToTTF — no extra build step or
// external tooling required.
func NewRenderer(ctx context.Context) (*Renderer, error) {
	ttf, err := woffToTTF(woffData)
	if err != nil {
		return nil, fmt.Errorf("decode embedded woff: %w", err)
	}

	rctx, err := resvg.NewContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("resvg context: %w", err)
	}
	rr, err := rctx.NewRenderer()
	if err != nil {
		_ = rctx.Close()
		return nil, fmt.Errorf("resvg renderer: %w", err)
	}
	if err := rr.LoadFontData(ttf); err != nil {
		_ = rctx.Close()
		return nil, fmt.Errorf("load embedded font: %w", err)
	}
	return &Renderer{ctx: rctx, r: rr}, nil
}

// Close tears down the wasm runtime. Idempotent: a second call is a no-op.
func (r *Renderer) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctx == nil {
		return nil
	}
	err := r.ctx.Close()
	r.ctx = nil
	r.r = nil
	return err
}

// RenderPNG expands the SVG template for `in` and rasterises it to PNG bytes
// at OutputWidth x OutputHeight. Recovers from panics inside the wasm runtime
// so a malformed glyph or resvg bug returns an error to the caller without
// taking the whole server down.
func (r *Renderer) RenderPNG(in Inputs) (png []byte, err error) {
	svg, err := expandSVG(in)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.r == nil {
		return nil, fmt.Errorf("renderer is closed")
	}

	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("resvg render panic: %v", rec)
			png = nil
		}
	}()

	out, err := r.r.RenderWithSize(svg, OutputWidth, OutputHeight)
	if err != nil {
		return nil, fmt.Errorf("resvg render: %w", err)
	}
	return out, nil
}

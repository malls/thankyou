package render

import (
	"context"
	"fmt"

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
// The renderer is backed by a fixed-size pool of resvg instances. Each instance
// owns its own resvg.Context (a wazero runtime + wasm module) and a single
// resvg.Renderer with the embedded font preloaded. Concurrent RenderPNG calls
// pull an instance from a buffered channel, run the wasm rasterisation, and
// return the instance to the channel — the channel doubles as a counted
// semaphore so we never exceed `size` concurrent renders.
//
// Why one Context per renderer (not many renderers off one Context):
// resvg-go v0.0.1's renderer-scoped wasm calls share the Context's wasm linear
// memory and the package-level malloc/free shims. Two goroutines calling into
// the same Context would race on memory allocation and any wasm-internal global
// state. One Context per pool slot is the only safe option without auditing or
// patching the wasm internals. The boot cost is paid once at startup.
//
// resvg-go v0.0.1 has a bug in (*Renderer).Close — it calls the underlying
// wasm `__renderer_delete` with no arguments even though the export expects
// a pointer. Calling Close always errors. We avoid it by:
//
//   - never calling (*resvg.Renderer).Close;
//   - tearing down via (*resvg.Context).Close at shutdown, which frees the
//     whole wazero runtime (and, transitively, the renderer's allocations).
type Renderer struct {
	pool  chan *instance
	insts []*instance

	// renderFn is the closure that performs the actual rasterisation. The
	// production path initialises it to a wrapper around resvg's
	// RenderWithSize so it can be swapped in tests for a sleeping stub. Pays
	// for ~2 LoC of indirection on the hot path; in return the parallelism
	// tests don't have to spin a real wasm runtime to assert pool semantics.
	renderFn func(inst *instance, svg []byte) ([]byte, error)
}

// instance is one pool slot: a wasm Context + a renderer with the font loaded.
// The slot lives for the lifetime of the Renderer; we never tear individual
// instances down on a render error (resvg errors are typically per-document,
// not state-corrupting) and never replace them mid-flight.
type instance struct {
	ctx *resvg.Context
	r   *resvg.Renderer
}

// NewRenderer boots `size` resvg instances and returns a pool-backed Renderer.
// Each instance owns its own wazero runtime + wasm module + font database;
// the WOFF→TTF conversion runs once and the same byte slice is shared across
// every instance's LoadFontData call.
//
// `size` <= 0 is clamped to 1 (single-renderer mode, equivalent to the
// pre-pool behaviour). Sequential boot keeps partial-failure unwinding
// straightforward; at the default size the cost is well under a second.
//
// resvg's underlying font database (fontdb) accepts SFNT font formats
// (TTF/OTF/TTC) but not WOFF/WOFF2. The repo ships a .woff so we decompress
// it to SFNT in-process at boot via woffToTTF — no extra build step or
// external tooling required.
func NewRenderer(ctx context.Context, size int) (*Renderer, error) {
	if size <= 0 {
		size = 1
	}

	// One-time WOFF→TTF: the bytes are immutable; share across instances.
	ttf, err := woffToTTF(woffData)
	if err != nil {
		return nil, fmt.Errorf("decode embedded woff: %w", err)
	}

	insts := make([]*instance, 0, size)
	closePartial := func() {
		for _, in := range insts {
			_ = in.ctx.Close()
		}
	}
	for i := 0; i < size; i++ {
		rctx, err := resvg.NewContext(ctx)
		if err != nil {
			closePartial()
			return nil, fmt.Errorf("resvg context [%d]: %w", i, err)
		}
		rr, err := rctx.NewRenderer()
		if err != nil {
			_ = rctx.Close()
			closePartial()
			return nil, fmt.Errorf("resvg renderer [%d]: %w", i, err)
		}
		if err := rr.LoadFontData(ttf); err != nil {
			_ = rctx.Close()
			closePartial()
			return nil, fmt.Errorf("load embedded font [%d]: %w", i, err)
		}
		insts = append(insts, &instance{ctx: rctx, r: rr})
	}

	pool := make(chan *instance, size)
	for _, in := range insts {
		pool <- in
	}
	r := &Renderer{
		pool:  pool,
		insts: insts,
	}
	r.renderFn = func(inst *instance, svg []byte) ([]byte, error) {
		return inst.r.RenderWithSize(svg, OutputWidth, OutputHeight)
	}
	return r, nil
}

// Close tears down every pool instance's wasm Context. (*resvg.Renderer).Close
// is intentionally not called — broken in resvg-go v0.0.1; see file header.
// Idempotent: a second call is a no-op.
func (r *Renderer) Close() error {
	if r.insts == nil {
		return nil
	}
	var firstErr error
	for _, in := range r.insts {
		if err := in.ctx.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	r.insts = nil
	r.pool = nil
	return firstErr
}

// RenderPNG expands the SVG template for `in` and rasterises it to PNG bytes
// at OutputWidth x OutputHeight. Acquires one pool slot for the wasm call;
// blocks if every slot is busy.
//
// The supplied ctx is checked at acquire time so a client whose context was
// cancelled (the 60s WriteTimeout, an explicit disconnect, etc.) returns
// promptly without burning a slot. The wasm call itself is **not** ctx-aware;
// per-call render timeout is TYB-20.
//
// Recovers from panics inside the wasm runtime so a malformed glyph or resvg
// bug returns an error to the caller without taking the whole server down.
func (r *Renderer) RenderPNG(ctx context.Context, in Inputs) (png []byte, err error) {
	svg, err := expandSVG(in)
	if err != nil {
		return nil, err
	}

	if r.pool == nil {
		return nil, fmt.Errorf("renderer is closed")
	}

	var inst *instance
	select {
	case inst = <-r.pool:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { r.pool <- inst }()

	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("resvg render panic: %v", rec)
			png = nil
		}
	}()

	out, rerr := r.renderFn(inst, svg)
	if rerr != nil {
		return nil, fmt.Errorf("resvg render: %w", rerr)
	}
	return out, nil
}

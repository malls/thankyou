# TYB-16: Renderer pool to lift the 1-render-at-a-time ceiling

## Context

Today `server/internal/render/render.go` holds a single `*resvg.Renderer` behind a `sync.Mutex`. Every call to `RenderPNG` (used by `/api/render`, `/api/checkout/start`, and `/api/printful/products`) holds that mutex start-to-finish for 0.5–2s of wasm rasterisation. The audit (TYB-12-followup) measured the result: a site-wide ~1 req/s ceiling, with a 50-design burst tail-latencying at 50× single-render time.

The package comment at `render.go:35-36` already pre-approves a pool of renderers as the fix. This task implements it.

Pairs with TYB-20 (per-call timeout): we explicitly do **not** plumb `context.Context` into the wasm call here — that's TYB-20's job. We do plumb it on the acquire side, so a client cancelling mid-queue gives up gracefully without burning a renderer slot.

## Investigation findings

### resvg-go v0.0.1 `Close()` is structurally broken — confirmed

`/Users/forrest/go/pkg/mod/github.com/kanrichan/resvg-go@v0.0.1/resvg.go:92-103`:

```go
func (r *Renderer) Close() error {
    fn := r.ctx.mod.ExportedFunction("__renderer_delete")
    if fn == nil { return errWasmFunctionNotFound }
    _, err := fn.Call(r.ctx)   // <-- no r.ptr passed!
    if err != nil { return err }
    return nil
}
```

Every other renderer-scoped wasm call (`RenderWithSize`, `LoadFontData`, `SetDpi`, ...) passes `api.EncodeI32(r.ptr)` as the first argument. `Close()` does not. The wasm export expects a renderer pointer; calling it with zero args either errors at the wasm boundary or, worse, deletes the renderer at slot 0. The existing render.go comment at lines 26-32 documents this and our existing strategy of avoiding `(*resvg.Renderer).Close()` entirely. The pool will continue avoiding it.

### Multiple `*resvg.Renderer` instances per Context — verified

Each `Renderer` struct (`resvg.go:41-44`) carries its own `ptr int32` returned from `__renderer_new`. The Context holds the wazero runtime + module (and thus the wasm linear memory). **All renderers off one Context share the same wasm linear memory and the same `__wasm_bytes_malloc`/`Memory().Write`/`Memory().Read`/`__wasm_bytes_free` flow.** Concurrent calls into the same Context would race on memory allocation and any wasm-internal global state.

**Decision:** the pool must use **one Context per renderer**, not one Context with many renderers off it. Each pool slot owns its own wazero runtime, its own wasm module instance, its own font db. This is the only safe option without auditing/patching the wasm internals.

### Per-instance memory cost

The wasm module is `~5MB gzipped` (the `wasm/resvg.wasm.gz` embed). Decompressed it lives in the wazero runtime's compiled-code cache. Plus per-instance: a font db with our ~250KB Helvetica TTF, and the renderer state in linear memory. Rough estimate: 10-30 MB resident per pool slot. At N=4 that's ~80MB worst case, comfortable on any host that already runs this Go server.

### Boot cost

`NewContext` is ~50ms today (per the `render.go:21-22` comment). `LoadFontData` is fast (the WOFF→TTF conversion runs once and the bytes are passed in). At N=4, sequential boot is ~200-500ms; parallelisable with a small `errgroup` if it ever matters. Acceptable to do sequentially in V1 — it's only a startup cost.

The WOFF→TTF conversion (`woffToTTF` in `woff.go`) operates on immutable bytes. **We compute it once and pass the same `[]byte` to every renderer's `LoadFontData`** — saving N-1 zlib decompressions at boot.

### Existing render tests still pass

`render_test.go` constructs a renderer with `NewRenderer(context.Background())` and calls `RenderPNG` directly. The pool API will keep `*Renderer` and `RenderPNG(in Inputs) ([]byte, error)` exactly as today — just internally backed by a pool. With a default pool size of 1 in tests (or whatever `RENDERER_POOL_SIZE` defaults to in main.go, which doesn't apply in unit tests), the existing golden test runs unchanged. Confirmed deterministic: resvg's output is byte-identical for the same input on the same Go/resvg-go version.

## Public API: before / after

### Before (`render.go`)

```go
type Renderer struct {
    mu  sync.Mutex
    ctx *resvg.Context
    r   *resvg.Renderer
}

func NewRenderer(ctx context.Context) (*Renderer, error)
func (r *Renderer) Close() error
func (r *Renderer) RenderPNG(in Inputs) (png []byte, err error)
```

### After (`render.go`)

```go
type Renderer struct {
    pool chan *instance     // buffered, capacity == pool size
    insts []*instance       // for shutdown sweep
}

// instance is the per-slot pair of wasm Context + Renderer. Lives for the
// lifetime of the Renderer; never returned to the pool in a broken state.
type instance struct {
    ctx *resvg.Context
    r   *resvg.Renderer
}

// NewRenderer boots `size` resvg instances. size <= 0 is clamped to 1.
// Each instance owns its own wazero runtime + wasm module + font db; the
// shared WOFF→TTF conversion happens once at the top.
func NewRenderer(ctx context.Context, size int) (*Renderer, error)

// Close tears down every instance's wasm Context. The per-renderer Close
// is skipped (broken in resvg-go v0.0.1; see file header). Idempotent.
func (r *Renderer) Close() error

// RenderPNG acquires a pool slot, runs the wasm rasterisation, and returns
// the slot. Blocks if all N slots are busy. The supplied ctx is checked at
// acquire time (to give cancelled clients a fast path); it is NOT plumbed
// into the wasm call itself — that's TYB-20's job.
func (r *Renderer) RenderPNG(ctx context.Context, in Inputs) (png []byte, err error)
```

**Caller signature change:** `RenderPNG` gains a `context.Context` first arg. Three call sites update:

- `server/internal/httpserver/handlers.go:110` — `Render` handler. Pass `r.Context()` from the `*http.Request`.
- `server/internal/httpserver/checkout_handlers.go:154` — pass the request context.
- `server/internal/httpserver/printful_handlers.go:162, 366` — pass the request context.

The render closures handed to `Store.SaveDedup` capture the request context; `SaveDedup`'s `produce func() ([]byte, error)` signature stays unchanged.

The render_test.go construction site updates to `NewRenderer(context.Background(), 1)`. The single test call site `r.RenderPNG(in)` becomes `r.RenderPNG(context.Background(), in)`.

## Pool design

### Channel-backed semaphore + pre-populated

```go
func (r *Renderer) RenderPNG(ctx context.Context, in Inputs) (png []byte, err error) {
    svg, err := expandSVG(in)
    if err != nil {
        return nil, err
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

    out, rerr := inst.r.RenderWithSize(svg, OutputWidth, OutputHeight)
    if rerr != nil {
        return nil, fmt.Errorf("resvg render: %w", rerr)
    }
    return out, nil
}
```

Why a buffered channel and not `sync.Pool`: instances have expensive construction with side effects (wasm runtime + font load). `sync.Pool`'s GC-driven eviction would silently destroy them, forcing reconstruction on the hot path. A fixed channel is the idiomatic Go semaphore pattern.

Why pre-populated (not lazy): the audit's whole point is throughput; lazy first-use construction would push the boot cost onto a real user request. We pay it once at startup.

### Acquire-side context cancellation

The `select` between `<-r.pool` and `<-ctx.Done()` lets a client whose context was cancelled (via the 60s `WriteTimeout`, an explicit client disconnect, etc.) return `ctx.Err()` instead of waiting for a slot. This frees handler goroutines under burst load. **We deliberately do not propagate ctx into the wasm call** — that's TYB-20.

### Error handling: V1 conservative

If `RenderWithSize` returns an error, we return the instance to the pool and surface the error. The instance is presumed healthy — resvg errors are typically per-document (malformed SVG, font lookup failure) rather than corrupting the wasm state. If the wasm panics, the existing `recover()` runs and we still return the instance. This matches today's single-renderer behavior: a wasm-internal corruption would have taken the whole server down anyway, and now it would too. Out of scope for V1: poison-pill replacement, instance recycling.

## Configuration

### New env var: `RENDERER_POOL_SIZE`

- **Default when unset or empty:** `4`. Memory-bound not CPU-bound (each slot carries its own wasm runtime); a fixed default avoids over-provisioning on large hosts.
- **Clamping:** if the parsed value is `<= 0` or fails to parse, log a warning and clamp to `1`. Don't fatal-fail boot for a typo.
- **Wired in:** `server/cmd/server/main.go` reads `RENDERER_POOL_SIZE` next to the existing env vars (around `port`/`dataDir`), parses it, applies clamping, and passes it as the new second arg to `render.NewRenderer`.
- **Logged at startup:** one line, e.g. `renderer pool size: 4`. When N=1, log `renderer pool size: 1 (no parallelism; raise RENDERER_POOL_SIZE for throughput)` so operators don't accidentally leave a debug setting in place.

### Documentation

Both files get one-line additions:

- `.env.example`: add at the top with `PORT`/`DATA_DIR`:
  ```
  # Number of parallel render worker slots. Each holds a wasm runtime + font
  # database (~10-30MB resident). Default 4. Lift this if /api/render queues.
  RENDERER_POOL_SIZE=4
  ```
- `README.md` env-var bullet list (around line 29-39): add `- RENDERER_POOL_SIZE — number of parallel render slots (default 4). Each slot is a separate wasm runtime; raise this if render-bound endpoints (/api/render, /api/checkout/start, /api/printful/products) start queueing.`
- `server/cmd/server/main.go` package-level doc-comment env-var section: add the same description.

## Construction strategy

```go
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
    for i := 0; i < size; i++ {
        rctx, err := resvg.NewContext(ctx)
        if err != nil {
            // unwind partial pool
            for _, in := range insts { _ = in.ctx.Close() }
            return nil, fmt.Errorf("resvg context [%d]: %w", i, err)
        }
        rr, err := rctx.NewRenderer()
        if err != nil {
            _ = rctx.Close()
            for _, in := range insts { _ = in.ctx.Close() }
            return nil, fmt.Errorf("resvg renderer [%d]: %w", i, err)
        }
        if err := rr.LoadFontData(ttf); err != nil {
            _ = rctx.Close()
            for _, in := range insts { _ = in.ctx.Close() }
            return nil, fmt.Errorf("load embedded font [%d]: %w", i, err)
        }
        insts = append(insts, &instance{ctx: rctx, r: rr})
    }

    pool := make(chan *instance, size)
    for _, in := range insts { pool <- in }
    return &Renderer{pool: pool, insts: insts}, nil
}
```

Sequential boot (not concurrent) is fine — it's a one-time cost and at N=4 it's < 250ms. Easier to reason about partial-failure cleanup.

## Lifecycle / shutdown

```go
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
```

We close every instance's wasm `Context` (which is what frees the wazero runtime). We do **not** call `(*resvg.Renderer).Close()` — broken in v0.0.1 as confirmed above. Idempotent: nil check at top.

The `defer renderer.Close()` in `main.go:88-91` keeps working unchanged.

## Tests

### New tests in `server/internal/render/render_test.go`

1. **`TestPool_ParallelRendersUpToSize`** — construct N=2 renderer, fire 3 goroutines that each call `RenderPNG`. Use a high-water-mark counter (atomic increment on entry, decrement on exit) to assert max-concurrent reached exactly 2. The third goroutine must enter only after one of the first two exits. Implemented via a package-private `renderFn` field on `Renderer` that production code initializes to a closure over `inst.r.RenderWithSize`; tests override it to a sleeping stub. Adds 1-2 LoC of indirection on the prod path; pays for itself in test reliability.

2. **`TestPool_AcquireContextCancellation`** — N=1 renderer with the slot held by a slow-stub render; second goroutine calls `RenderPNG` with an already-cancelled context. Asserts: returns `context.Canceled` promptly (within 50ms), without waiting for the held slot to free.

3. **`TestPool_SizeClamping`** — call `NewRenderer(ctx, 0)` and `NewRenderer(ctx, -3)`. Both succeed and produce a working N=1 renderer. The package log line confirms size=1.

4. **`TestPool_GoldenStillPasses`** — the existing `TestRenderPNG_GoldenFOOBAR` is updated to construct with `NewRenderer(ctx, 1)` and pass `context.Background()` to `RenderPNG`. The golden bytes don't change. Effectively the existing test renamed/updated, not a new one.

### Existing tests update

- `render_test.go` test sites: `NewRenderer(context.Background())` → `NewRenderer(context.Background(), 1)`. `r.RenderPNG(in)` → `r.RenderPNG(context.Background(), in)`.
- `httpserver/*_test.go`: any test that constructs a `*render.Renderer` updates the same way; tests that mock the renderer via interface need no changes (none today, all tests use the real `*render.Renderer`).

### Acceptance: `go test ./...` from `server/` is green.

## Acceptance criteria

1. With `RENDERER_POOL_SIZE=4`, four concurrent calls to `RenderPNG` execute in parallel: peak concurrent in-flight render count == 4 (verified by the parallel test).
2. With N=4 and 5 concurrent requests, the 5th waits in the channel until one of the first 4 returns its slot.
3. Default pool size when env unset == 4. Logged at startup.
4. `RENDERER_POOL_SIZE=0`, `=-1`, `=foo`, or unparseable: clamps to 1 with a warning log; server still boots.
5. Acquire-side ctx cancellation: a request whose context is cancelled while waiting for a slot returns `ctx.Err()` promptly (well under one render time).
6. The wasm call itself is **not** ctx-aware (deferred to TYB-20). Existing recover-from-panic remains.
7. All existing render and httpserver tests pass with the API change. Golden PNG bytes unchanged.
8. `go test ./...` and `go vet ./...` from `server/` are green.
9. README env-var section updated. `.env.example` updated. main.go env-var doc-comment updated.
10. Server shutdown closes every instance's wasm Context; no goroutine/wasm leaks (manually verified by sending SIGINT mid-burst and checking the log).

## Out of scope

- Per-call render timeout (TYB-20).
- Pool resize at runtime / SIGHUP.
- Adaptive sizing under load.
- Backpressure beyond what the bounded pool already provides (HTTP-level admission control would be a separate task).
- Replacing/recycling instances after a wasm panic. Today a wasm panic propagates up via `recover()`; if it left the wasm state corrupt, that slot is poisoned but the rest of the pool keeps working. Rare enough to defer.
- Sharing the wazero Runtime across instances. Investigated; resvg-go v0.0.1 doesn't expose a path to make this safe (linear memory is per-module-instance; the renderer ptr lives in that memory).
- Calling `(*resvg.Renderer).Close()` — still broken in v0.0.1; we still don't call it.

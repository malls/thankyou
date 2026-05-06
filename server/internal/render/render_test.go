package render

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// updateGolden lets us regenerate the golden fixture without manually copying
// PNGs from the data dir. Run as: go test ./internal/render -update.
var updateGolden = flag.Bool("update", false, "regenerate golden test fixtures")

// TestRenderPNG_GoldenFOOBAR is a regression check on the SVG template. The
// SVG layout (viewBox math, font-size, dy values, footer offset) is finicky
// to tune visually — once we've picked values that look right, this test
// pins the output bytes so an accidental change to the template produces a
// loud failure. resvg is deterministic across runs on the same Go and
// resvg-go versions; if either is upgraded and intentionally changes the
// output, regenerate with -update.
//
// The golden file lives at server/testdata/golden_foo_bar.png so regenerating
// it from anywhere in the tree resolves to the same path via filepath.Join
// + go-test's automatic cwd shift to the package dir.
func TestRenderPNG_GoldenFOOBAR(t *testing.T) {
	r, err := NewRenderer(context.Background(), 1)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Logf("Close: %v", err)
		}
	})

	in, err := Validate("FOO", "BAR", "")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	got, err := r.RenderPNG(context.Background(), in)
	if err != nil {
		t.Fatalf("RenderPNG: %v", err)
	}

	goldenPath := filepath.Join("..", "..", "testdata", "golden_foo_bar.png")
	if *updateGolden {
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("regenerated %s (%d bytes)", goldenPath, len(got))
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v (run `go test ./internal/render -update` to create it)", err)
	}

	if !bytes.Equal(got, want) {
		t.Fatalf("rendered PNG (%d bytes) does not match golden (%d bytes). "+
			"If the template change is intentional, regenerate with: "+
			"go test ./internal/render -update", len(got), len(want))
	}
}

// TestValidate_TruncatesAtRuneLimit makes sure the maxlength=10 rule mirrors
// the client's validation — a stray 11-char input from a non-browser caller
// (curl, an automated retry) should 400, not silently render a clipped image.
func TestValidate_TruncatesAtRuneLimit(t *testing.T) {
	cases := []struct {
		name      string
		main      string
		middle    string
		wantError bool
		wantField string
	}{
		{"short ok", "FOO", "BAR", false, ""},
		{"exactly 10", "ABCDEFGHIJ", "1234567890", false, ""},
		{"11 main rejected", "ABCDEFGHIJK", "BAR", true, "text"},
		{"11 middle rejected", "FOO", "ABCDEFGHIJK", true, "middletext"},
		{"unicode counts as runes", "AAAAAAAAAA", "ÉÉÉÉÉÉÉÉÉÉ", false, ""},
		{"unicode 11 runes rejected", "FOO", "ÉÉÉÉÉÉÉÉÉÉÉ", true, "middletext"},
		{"empty falls back to default", "", "", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, err := Validate(tc.main, tc.middle, "")
			if tc.wantError {
				if err == nil {
					t.Fatalf("expected error, got Inputs=%+v", in)
				}
				if ve, ok := err.(*ValidationError); ok {
					if ve.Field != tc.wantField {
						t.Errorf("error field = %q, want %q", ve.Field, tc.wantField)
					}
				} else {
					t.Errorf("error type = %T, want *ValidationError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.main == "" && tc.middle == "" && in.MainText != DefaultMainText {
				t.Errorf("empty input should default to %q, got %q", DefaultMainText, in.MainText)
			}
		})
	}
}

// TestHash_DeterministicAcrossCalls is a smoke check that the same Inputs
// always produce the same hash. The hash drives content-addressed storage,
// so any non-determinism here would silently break dedup and cache headers.
func TestHash_DeterministicAcrossCalls(t *testing.T) {
	in := Inputs{MainText: "FOO", MiddleText: "BAR", Background: "white"}
	h1 := Hash(in)
	h2 := Hash(in)
	if h1 != h2 {
		t.Fatalf("Hash not deterministic: %s != %s", h1, h2)
	}
	if len(h1) != 64 {
		t.Fatalf("Hash length = %d, want 64", len(h1))
	}
}

// TestPool_ParallelRendersUpToSize asserts the pool actually unlocks
// concurrent rasterisation: with size=2, three concurrent RenderPNG calls
// should see at most 2 in flight simultaneously, and the third must enter
// only after one of the first two exits. This is the audit's whole point —
// without the pool, max-in-flight is 1.
//
// The test substitutes Renderer.renderFn with a sleeping stub that increments
// a shared high-water-mark counter on entry and decrements on exit. We boot
// the renderer with size=2 (so it owns 2 wasm contexts; we don't exercise
// them, but it confirms the channel was sized correctly).
func TestPool_ParallelRendersUpToSize(t *testing.T) {
	r, err := NewRenderer(context.Background(), 2)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	const renderDuration = 80 * time.Millisecond
	var (
		inFlight    int32
		highWater   int32
		startBarrier sync.WaitGroup
	)
	startBarrier.Add(1)
	r.renderFn = func(inst *instance, svg []byte) ([]byte, error) {
		// Wait until all goroutines have been spawned, then race for the
		// pool. Without the barrier, goroutine 1 could finish before
		// goroutine 2 even reaches the acquire — high-water would only
		// hit 1 even with a working pool.
		startBarrier.Wait()
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			hw := atomic.LoadInt32(&highWater)
			if cur <= hw {
				break
			}
			if atomic.CompareAndSwapInt32(&highWater, hw, cur) {
				break
			}
		}
		time.Sleep(renderDuration)
		atomic.AddInt32(&inFlight, -1)
		return []byte("stub-png"), nil
	}

	in, err := Validate("FOO", "BAR", "")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}

	const goroutines = 3
	var wg sync.WaitGroup
	wg.Add(goroutines)
	errs := make([]error, goroutines)
	start := time.Now()
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			_, errs[i] = r.RenderPNG(context.Background(), in)
		}(i)
	}
	// Give the goroutines a moment to all reach the barrier, then unblock.
	time.Sleep(20 * time.Millisecond)
	startBarrier.Done()
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Errorf("goroutine %d: %v", i, e)
		}
	}
	hw := atomic.LoadInt32(&highWater)
	if hw != 2 {
		t.Errorf("peak concurrent renders = %d, want 2 (size of pool)", hw)
	}
	// 3 goroutines through a size-2 pool should take roughly 2× renderDuration
	// (ceil(3/2)). With size-1 it would take 3×. Use a generous slack — CI
	// schedulers are noisy.
	wall := time.Since(start)
	if wall >= 3*renderDuration {
		t.Errorf("wall time %v >= 3×renderDuration; pool not parallelising", wall)
	}
}

// TestPool_AcquireContextCancellation: when every slot is busy, a request
// whose context is already cancelled must return ctx.Err() promptly without
// waiting for the slot to free. This is the burst-load fast path — clients
// who have given up shouldn't keep their handler goroutine pinned.
func TestPool_AcquireContextCancellation(t *testing.T) {
	r, err := NewRenderer(context.Background(), 1)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	hold := make(chan struct{})
	r.renderFn = func(inst *instance, svg []byte) ([]byte, error) {
		<-hold
		return []byte("stub"), nil
	}

	in, err := Validate("FOO", "BAR", "")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}

	// Goroutine 1 grabs the only slot and holds it.
	holderDone := make(chan struct{})
	go func() {
		defer close(holderDone)
		_, _ = r.RenderPNG(context.Background(), in)
	}()
	// Wait for the holder to be inside renderFn before we attempt the
	// cancelled acquire — otherwise we might steal the slot first.
	time.Sleep(20 * time.Millisecond)

	cctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err = r.RenderPNG(cctx, in)
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("RenderPNG with cancelled ctx: err=%v, want context.Canceled", err)
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("RenderPNG with cancelled ctx took %v, want < 50ms", elapsed)
	}

	close(hold)
	<-holderDone
}

// TestPool_SizeClamping: NewRenderer(ctx, 0) and NewRenderer(ctx, -3) must
// still produce a working renderer (clamped to size=1) rather than failing
// boot. A typo in RENDERER_POOL_SIZE shouldn't take the server down.
func TestPool_SizeClamping(t *testing.T) {
	for _, size := range []int{0, -3} {
		r, err := NewRenderer(context.Background(), size)
		if err != nil {
			t.Fatalf("NewRenderer(ctx, %d): %v", size, err)
		}
		if cap(r.pool) != 1 {
			t.Errorf("size=%d: pool cap = %d, want 1", size, cap(r.pool))
		}
		if len(r.insts) != 1 {
			t.Errorf("size=%d: insts len = %d, want 1", size, len(r.insts))
		}
		// A real render must still work after clamping.
		in, _ := Validate("FOO", "BAR", "")
		if _, err := r.RenderPNG(context.Background(), in); err != nil {
			t.Errorf("size=%d: RenderPNG after clamp: %v", size, err)
		}
		_ = r.Close()
	}
}

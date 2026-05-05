package render

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"
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
	r, err := NewRenderer(context.Background())
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
	got, err := r.RenderPNG(in)
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

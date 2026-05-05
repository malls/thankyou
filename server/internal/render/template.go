package render

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"unicode/utf8"
)

// templateSVG is the SVG document with Go template placeholders. Mirrors the
// inline SVG in the repo-root index.html, with viewBox+width+height committed
// (the in-browser SVG has none and relies on getBBox at runtime, which resvg
// can't do — see TYB-6 plan section 3).
//
// We deliberately do NOT inline an @font-face data URI here: resvg's font
// database is fed the decoded font at server boot (see render.go), and
// resolves font-family lookups from the document against that database.
//
//go:embed template.svg
var templateSVG string

// woffData is the Helvetica Black woff baked into the binary so every render
// uses the exact font shipped with the site. The browser uses the same woff
// via the inline @font-face in index.html.
//
//go:embed Helvetica-Black.woff
var woffData []byte

// svgTmpl is parsed once at package init so render-path errors are syntax
// errors at startup rather than per-request surprises.
var svgTmpl = template.Must(template.New("print").Parse(templateSVG))

// templateData mirrors the {{...}} placeholders in template.svg.
type templateData struct {
	MainText      string
	MiddleText    string
	Background    string
	ViewBoxWidth  int
	ViewBoxHeight int
}

// SVG layout constants. Shared between the template (via templateData) and
// expandSVG so the geometry math sits in one place.
const (
	// charAdvance is the worst-case (uppercase Helvetica Black) per-char
	// horizontal advance at font-size 320 with letter-spacing -25, in user
	// units. Measured from 10-M renders. Wide letters (M, W) advance ~280
	// once the stroke around the filled middle line is included; narrower
	// letters less. Sized for the worst case so a 10-M middle line clears
	// the edge.
	charAdvance = 280

	// horizontalPadding is the right-side safety margin so the last char's
	// stroke doesn't touch the viewBox edge.
	horizontalPadding = 200

	// minViewBoxWidth keeps a tiny input (1-2 chars) from producing a viewBox
	// narrower than the footer line. The footer "thankyoubag.online" at
	// font-size 100 sits at x=200 and runs ~1100 units; rounded up with
	// padding so the URL is never the limiting horizontal element.
	minViewBoxWidth = 1400

	// viewBoxHeight is fixed across renders — 7 lines of dy=228 + first-line
	// ascent + footer line + descender. See the comment in template.svg.
	viewBoxHeight = 1880
)

// expandSVG fills the SVG template with the validated Inputs. The middle line
// falls back to MainText to match the client behavior (filled-text shows the
// main text when the middle input is empty).
//
// The viewBox width is computed from the longer of MainText and MiddleText so
// short inputs get a tight crop (text fills the canvas after rasterisation)
// and long inputs get extra width. This mirrors the browser's getBBox-driven
// dynamic SVG sizing — resvg can't do that at parse time, so we precompute it.
//
// html/template auto-escapes <, >, &, ", ' which is exactly what we want —
// SVG text content uses XML escaping rules, identical to HTML for these chars.
func expandSVG(in Inputs) ([]byte, error) {
	middle := in.MiddleText
	if middle == "" {
		middle = in.MainText
	}
	maxLen := utf8.RuneCountInString(in.MainText)
	if l := utf8.RuneCountInString(middle); l > maxLen {
		maxLen = l
	}
	width := maxLen*charAdvance + horizontalPadding
	if width < minViewBoxWidth {
		width = minViewBoxWidth
	}
	data := templateData{
		MainText:      in.MainText,
		MiddleText:    middle,
		Background:    in.Background,
		ViewBoxWidth:  width,
		ViewBoxHeight: viewBoxHeight,
	}
	var buf bytes.Buffer
	buf.Grow(len(templateSVG) + 256)
	if err := svgTmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("expand svg template: %w", err)
	}
	return buf.Bytes(), nil
}

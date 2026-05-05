package render

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
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
	MainText   string
	MiddleText string
	Background string
}

// expandSVG fills the SVG template with the validated Inputs. The middle line
// falls back to MainText to match the client behavior (filled-text shows the
// main text when the middle input is empty).
//
// html/template auto-escapes <, >, &, ", ' which is exactly what we want —
// SVG text content uses XML escaping rules, identical to HTML for these chars.
func expandSVG(in Inputs) ([]byte, error) {
	middle := in.MiddleText
	if middle == "" {
		middle = in.MainText
	}
	data := templateData{
		MainText:   in.MainText,
		MiddleText: middle,
		Background: in.Background,
	}
	var buf bytes.Buffer
	buf.Grow(len(templateSVG) + 256)
	if err := svgTmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("expand svg template: %w", err)
	}
	return buf.Bytes(), nil
}

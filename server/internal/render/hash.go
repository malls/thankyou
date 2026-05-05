// Package render builds the print-ready PNG for a given text/middletext pair.
//
// The pipeline is: validate inputs -> normalize -> hash -> expand SVG template
// -> rasterise via resvg-go -> PNG bytes. The hash is content-addressed so the
// file store and HTTP cache can key on it deterministically.
package render

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// TemplateVersion changes whenever the SVG template structure changes in a
// way that affects pixels. Bumping it invalidates every cached file in one
// move (since the version is a hash input). See TYB-6 plan section 6.
const TemplateVersion = "v1"

// MaxInputLen mirrors the maxlength=10 attribute on the two HTML inputs.
const MaxInputLen = 10

// DefaultMainText is the placeholder the client init() falls back to when both
// fields are empty (see script.js DEFAULT_COPY).
const DefaultMainText = "THANK YOU"

// DefaultBackground is the SVG <rect> fill colour. Locked to "white" for the
// prototype; a `?bg=` knob can be added when the human says so.
const DefaultBackground = "white"

// Inputs is the validated, normalized payload passed to the SVG template and
// hashed. All string fields are uppercase, NFC-normalized, and stripped of
// control characters.
type Inputs struct {
	MainText   string
	MiddleText string
	Background string
}

// ErrTextTooLong is returned by Validate when a field exceeds MaxInputLen
// runes (we count runes, not bytes, so emoji/accents don't trigger early).
var ErrTextTooLong = errors.New("text exceeds maximum length")

// Validate sanitizes raw client input and returns Inputs ready to hash and
// render. Trims whitespace, strips control chars, NFC-normalizes, uppercases.
// If both text fields are empty, MainText defaults to DefaultMainText
// (matching the client init).
//
// Returns ErrTextTooLong (wrapped with the offending field name) when either
// field is longer than MaxInputLen runes after sanitisation.
func Validate(rawMain, rawMiddle, rawBg string) (Inputs, error) {
	mainText := sanitize(rawMain)
	middleText := sanitize(rawMiddle)

	if utf8.RuneCountInString(mainText) > MaxInputLen {
		return Inputs{}, &ValidationError{Field: "text", Reason: "must be 10 characters or fewer"}
	}
	if utf8.RuneCountInString(middleText) > MaxInputLen {
		return Inputs{}, &ValidationError{Field: "middletext", Reason: "must be 10 characters or fewer"}
	}

	if mainText == "" && middleText == "" {
		mainText = DefaultMainText
	}

	bg := strings.ToLower(strings.TrimSpace(rawBg))
	if bg == "" {
		bg = DefaultBackground
	}
	// Tight allowlist for now. The client never sends this field; only curl
	// debug callers do, and we don't want to accept arbitrary CSS colours
	// that might break the SVG parse.
	if bg != "white" && bg != "transparent" {
		return Inputs{}, &ValidationError{Field: "background", Reason: "must be \"white\" or \"transparent\""}
	}

	return Inputs{MainText: mainText, MiddleText: middleText, Background: bg}, nil
}

// ValidationError is a typed 400 carrier so handlers can propagate field+reason
// to the client without leaking server internals.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return e.Field + ": " + e.Reason
}

// sanitize strips ASCII control chars (\x00-\x1F, \x7F), trims surrounding
// whitespace, NFC-normalizes (so equivalent Unicode forms hash the same), and
// uppercases. The CSS does text-transform: uppercase client-side; we mirror
// that server-side so the hash is over the rendered text, not the raw input.
func sanitize(s string) string {
	// NFC first so combining marks resolve before we uppercase; this matters
	// for inputs like "Café" which would otherwise differ from "Café".
	s = norm.NFC.String(s)

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		// unicode.IsControl catches ASCII controls and the C1 range. We
		// deliberately do NOT call IsPrint — we want to keep regular punctuation,
		// emoji, etc.; only the truly invisible bytes are dropped.
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return strings.ToUpper(strings.TrimSpace(b.String()))
}

// Hash computes the deterministic content hash for an Inputs value. Same
// inputs -> same hash, across processes/machines/Go versions. Encoded as a
// 64-char lowercase hex string suitable for use as a filename and URL path.
//
// Layout: {version}\x1f{main}\x1f{middle}\x1f{background}, sha256, hex.
// \x1f (Unit Separator) is a non-printable that won't collide with any rune
// the user can type, so the boundaries are unambiguous.
func Hash(in Inputs) string {
	canonical := strings.Join([]string{
		TemplateVersion,
		in.MainText,
		in.MiddleText,
		in.Background,
	}, "\x1f")
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

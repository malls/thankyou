// Package public exposes the static-site assets baked into the server binary
// at compile time via //go:embed. Lives at server/public/ so the assets sit
// in a single, top-level, version-controlled directory; the httpserver
// package imports this package and serves the FS at the root.
//
// The package exists primarily because //go:embed patterns cannot traverse
// upward (no `..`), so an embed directive in internal/httpserver/static.go
// can't reach a sibling-of-server directory. Putting the embed in this
// package places the .go file next to the assets it embeds.
package public

import (
	"embed"
	"io/fs"
)

// assets is the embedded FS containing every file we ship as the public
// static site. The pattern is explicit (one extension per glob) so a stray
// editor backup or .DS_Store does not accidentally land in the binary.
//
//go:embed *.html *.css *.js *.ico *.png *.woff *.woff2
var assets embed.FS

// FS returns the embedded assets as an fs.FS rooted at this package's
// directory. Callers (notably httpserver.NewRouter) wrap it in
// http.FS(...) and hand it to http.FileServer for "/" fall-through.
func FS() fs.FS {
	return assets
}

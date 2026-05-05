package httpserver

import (
	"embed"
	"io/fs"
)

// staticFS holds the static-site assets baked into the binary at compile
// time. Populated by tools/copy-static.sh which mirrors the repo-root
// files into server/static/ before `go build`. We embed the directory rather
// than individual files so adding a new asset is just a copy-static change.
//
//go:generate sh -c "../../tools/copy-static.sh"
//go:embed static/*
var staticFS embed.FS

// StaticFS returns an fs.FS rooted at `static/` (the embed prefix is
// stripped) so http.FileServer can serve the files at /<filename> directly.
// Returns an error if the embed subtree can't be opened — should never
// happen at runtime since the contents are baked in.
func StaticFS() (fs.FS, error) {
	return fs.Sub(staticFS, "static")
}

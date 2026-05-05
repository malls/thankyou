#!/usr/bin/env bash
# Copies the static-site assets from the repo root into server/static/ so
# they can be baked into the Go binary via //go:embed.
#
# Run from the server/ directory: ./tools/copy-static.sh
# Or via `go generate ./...` (see internal/httpserver/static.go).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SERVER_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd "$SERVER_DIR/.." && pwd)"
# The embed directive in internal/httpserver/static.go reads from a sibling
# `static/` directory — go:embed paths are relative to the .go file. Copy
# the assets there so they're baked into the binary at build time.
STATIC_DIR="$SERVER_DIR/internal/httpserver/static"

mkdir -p "$STATIC_DIR"

# Files we ship with the embedded site. Listed explicitly so the embed
# manifest is auditable and never picks up stray repo-root files.
FILES=(
  "index.html"
  "style.css"
  "script.js"
  "favicon.ico"
  "splash.png"
  "Helvetica-Black.woff"
  "Helvetica-Black.woff2"
)

for f in "${FILES[@]}"; do
  if [[ ! -f "$REPO_ROOT/$f" ]]; then
    echo "copy-static: missing $f at repo root" >&2
    exit 1
  fi
  cp "$REPO_ROOT/$f" "$STATIC_DIR/$f"
done

echo "copy-static: refreshed $STATIC_DIR from $REPO_ROOT"

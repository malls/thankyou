# TYB-7: Document server architecture and run steps in root README

Replace the current 3-line [README.md](../../README.md) with a 100-150 line document. Sections, in order:

## 1. Header / tagline (3-5 lines)
- Keep current first sentence verbatim: *"GitHub Pages site for generating text in the classic 'THANK YOU' plastic bag style."*
- Add one sentence: now also has a Go server that renders print-quality PNGs from the same SVG template, paving the way for fulfillment.
- Live-site line: keep `Hosted at [thankyoubag.online](https://thankyoubag.online).` exactly as today.

## 2. Run the server locally (~15-20 lines)
- One-line summary: server bakes the static site + font into a single binary; no dependencies beyond Go.
- Numbered steps (impl agent fills in exact commands, verifies against the actual code):
  1. Clone + `cd server`.
  2. `./tools/copy-static.sh` — copies repo-root assets into `server/internal/httpserver/static/` for `//go:embed`. Note this must be re-run after editing root-level static files.
  3. `go run ./cmd/server` — listens on `:8080` (override with `PORT` env var; data dir override is `DATA_DIR`).
  4. Verify: `curl http://localhost:8080/healthz` returns `ok`; visit `http://localhost:8080/` for the static site; `POST /api/render` with `{"text":"FOO","middletext":"BAR"}` returns `{file_id, url}` and writes `data/files/{hash}.png`.
- Mention `go test ./...` runs unit + golden-file tests.

## 3. Architecture (~40-50 lines, the meat)
Bullet-style, each decision one short paragraph or 2-3 bullets. No long prose.

- **Two pieces today.** Static site at the repo root (the GitHub Pages deploy that powers `thankyoubag.online`); new Go server in `server/` that embeds and re-serves that same site plus adds a render API. They are not deployed together yet — the cutover is deferred.
- **Server-side SVG render (the load-bearing decision).** The browser builds a preview SVG and rasterises via Canvas; the server expands a near-identical Go `text/template` SVG and rasterises with [resvg-go](https://github.com/kanrichan/resvg-go). The client POSTs `{text, middletext}` and the server hands back `{file_id, url}`. Rationale (3 sub-bullets): determinism across devices/zooms (browser canvas resampling drifts); content-addressed file URLs that survive page reloads; trust boundary — fulfillment artifacts shouldn't come from untrusted client bytes.
- **Why Go.** Glue server, not a render engine. Fast compile loop, single static binary, `//go:embed` ships the static site + woff in the binary, stdlib `net/http` is enough for three routes.
- **Why `resvg-go`.** Wraps Rust's `resvg` via wasm — best-quality SVG renderer reachable from Go. The font database is fed the decoded font once at boot; render calls are serialised through a mutex (the wasm renderer holds per-call state). Note: `Helvetica-Black.woff` is decompressed to TTF in-process at boot because resvg's font db doesn't accept WOFF directly.
- **Hash-keyed file store.** SHA-256 over canonicalised inputs (NFC-normalised, uppercased, plus a template version tag). Same design always returns the same `file_id`. `singleflight` dedupes concurrent identical requests; atomic temp-file rename keeps readers from seeing partial writes; `Cache-Control: public, max-age=31536000, immutable` on the served PNG.
- **Output dimensions.** Fixed 3600x4800 px (12"x16" at 300 DPI) — picks Bella+Canvas 3001 DTG print area now so files are Printful-ready when that integration lands. ViewBox width is per-input (computed from longest of `MainText`/`MiddleText`) so short inputs get a tight crop. Implementation notes: `font-size=320` and per-input viewBox were judgment calls during impl; flag for future readers.
- **What's deferred.** Printful API (mockups, sync products, orders); deploy + DNS cutover from GitHub Pages to the Go server.

## 4. Repo layout (~15 lines)
- A short tree, not exhaustive. Show:
  - Repo root: `index.html`, `style.css`, `script.js`, `Helvetica-Black.woff{,2}`, `splash.png`, `favicon.ico`, `CNAME` — the GitHub Pages site.
  - `server/`: `cmd/server/main.go`, `internal/render/`, `internal/httpserver/`, `internal/files/`, `tools/copy-static.sh`, `data/files/` (gitignored). One-line gloss on each subdir.
  - `.lattice/`: plans, tasks, sessions for the Lattice-tracked work.

## 5. Tasks tracked in Lattice (3-5 lines)
- One sentence: `.lattice/` holds the plans and task graph for ongoing work; run `lattice list` (or read `.lattice/plans/`) for current state. Do not enumerate task statuses.

## Constraints reminder for impl agent
- All file references use markdown link syntax (`[name](path)`).
- Don't promise endpoints/features that aren't built. The HTTP surface is exactly: `GET /healthz`, `POST /api/render`, `GET /api/files/{hash}.png`, plus static fall-through.
- Don't include verbatim Go/SVG snippets — describe behaviour, link to source.
- The orphaned `node_modules/` is gone from the tree; do NOT mention it as a deferred cleanup.
- Implementation deviated from both source plans on two minor points worth flagging in the architecture bullets: `font-size=320` (not `20em` as planned, mirrors browser's 20em at 16px root) and a *per-input* dynamic viewBox width (not a fixed `4096x3200`) — both improvements over what the plans specified.

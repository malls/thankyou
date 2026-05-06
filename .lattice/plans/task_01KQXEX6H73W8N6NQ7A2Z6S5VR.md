# TYB-14: Consolidate static assets into `server/public/`, drop GitHub Pages

## Goal

Collapse the duplicated static assets — currently shipped both at the repo root (for GitHub Pages) and copied into `server/internal/httpserver/static/` (for `//go:embed`) — into one canonical, version-controlled location at **`server/public/`**. Drop GitHub Pages entirely. The Go server becomes the only deploy target; `thankyoubag.online` will eventually be DNS-cut over to wherever the server is hosted (out of scope).

## Investigation findings (load-bearing)

1. **The `//go:embed` `..` constraint is real and binding.** `//go:embed` patterns must be siblings or descendants of the embedding `.go` file's package directory. They cannot traverse upward with `..`. So if the canonical assets live at `server/public/`, the embed cannot live at `server/internal/httpserver/static.go` — that file would need `//go:embed ../../public/*`, which Go rejects at compile time.

2. **The renderer's font embed is independent.** `server/internal/render/template.go` embeds `Helvetica-Black.woff` from a sibling file — `server/internal/render/Helvetica-Black.woff`, NOT from `server/internal/httpserver/static/`. Confirmed via `grep go:embed server/ --include="*.go"`:

   - `server/internal/render/template.go:20`: `//go:embed template.svg`
   - `server/internal/render/template.go:27`: `//go:embed Helvetica-Black.woff`
   - `server/internal/httpserver/static.go:14`: `//go:embed static/*`

   The render package owns its own copy of the WOFF. **Renaming/moving the httpserver static dir does not affect the renderer.** Render's `Helvetica-Black.woff` stays exactly where it is.

3. **Files duplicated today (tracked in git, verified via `git ls-files`):**

   At repo root: `index.html`, `style.css`, `script.js`, `favicon.ico`, `splash.png`, `Helvetica-Black.woff`, `Helvetica-Black.woff2`, `CNAME`.

   Copied into `server/internal/httpserver/static/`: same seven (no `CNAME`).

   Plus `server/internal/render/Helvetica-Black.woff` (independent, untouched).

4. **No `.github/` directory exists.** No GitHub Pages workflow file to delete; the GitHub Pages deploy is the default-branch-root flavor (no Action). Only `CNAME` is a GitHub Pages artifact.

5. **`copy-static.sh` references** (from `grep -rn copy-static`):
   - `README.md` lines 16, 19, 166 — instructions and "Repo layout" callout.
   - `.claude/settings.local.json` lines 28-29, 57 — auto-allow entries for executing the script. Cosmetic; safe to leave or strip later.
   - `server/internal/httpserver/static.go` line 13 — `//go:generate` directive.
   - `server/internal/httpserver/router.go` lines 51, 64, 70 — comment + error message.
   - `.lattice/plans/task_01KQVA5XQC3AB6S0YP449GNARY.md` — historical plan reference. Leave as-is (history).

6. **CLAUDE.md and `agents.md`** do not mention the dual-deploy story or `copy-static`. No changes required (verified via grep returning no hits for "GitHub Pages", "github pages", "copy-static", "github.io" in either file).

7. **`README.md` sections that change** (line numbers as of HEAD):
   - L3 (intro paragraph, "GitHub Pages site for...").
   - L7-50 ("Run the server locally" — drop step 2 entirely; renumber).
   - L107 ("Two pieces today" architecture bullet).
   - L148 ("What's deferred" — drop the GH-Pages-cutover phrase).
   - L150-167 ("Repo layout" — collapse the two-section layout into one; drop the CNAME bullet and the copy-static.sh bullet).

## Design decision: package layout

`//go:embed` cannot traverse `..`. The user explicitly asked for the canonical path to be `server/public/` (per the task description: "User confirmed: canonical path is server/public/"). The two real options that satisfy that path literally:

- **(a) Rename in place: `server/internal/httpserver/static/` → `server/internal/httpserver/public/`.** Smallest diff; the path "public/" is communicated *inside* httpserver, not at the top of `server/`. Does NOT match the user's literal stated path.

- **(d) New package at `server/public/`** — a new `embed.go` (or `public.go`) lives under `server/public/`, embeds its sibling assets via `//go:embed *.html *.css *.js *.png *.ico *.woff *.woff2` (or `//go:embed all:.`), and exposes `func FS() (fs.FS, error)`. The `httpserver` package imports it. Matches the user's literal path; one new ~25-line package; the directory at `server/public/` becomes a visible top-level "this is what we serve" location.

**Recommended: option (d).** The user told the orchestrator the canonical path is `server/public/`; option (a) doesn't deliver that. The added complexity is one tiny new package (`server/public/embed.go`) that does nothing but export an `embed.FS`. The httpserver static.go file can be deleted entirely and the router imports `…/server/public` directly — net code change is roughly neutral.

If the orchestrator prefers (a) for simplicity, the plan still works with trivial substitutions: the new package is replaced by an in-place rename, and the embed directive in `static.go` becomes `//go:embed public/*`. The acceptance criteria, README rewrite, and verification steps are otherwise identical. **Default to (d) unless the orchestrator pushes back.**

## Concrete file moves

Use `git mv` everywhere to preserve history. (Where `git mv` would land an asset on top of a duplicate that's already tracked, the destination is removed first via `git rm` then `git mv` lands the source — see "order of operations" below for the per-step recipe.)

| Source | Destination | Action |
|---|---|---|
| `index.html` (root) | `server/public/index.html` | `git mv` |
| `style.css` (root) | `server/public/style.css` | `git mv` |
| `script.js` (root) | `server/public/script.js` | `git mv` |
| `favicon.ico` (root) | `server/public/favicon.ico` | `git mv` |
| `splash.png` (root) | `server/public/splash.png` | `git mv` |
| `Helvetica-Black.woff` (root) | `server/public/Helvetica-Black.woff` | `git mv` |
| `Helvetica-Black.woff2` (root) | `server/public/Helvetica-Black.woff2` | `git mv` |
| `server/internal/httpserver/static/index.html` | — | `git rm` |
| `server/internal/httpserver/static/style.css` | — | `git rm` |
| `server/internal/httpserver/static/script.js` | — | `git rm` |
| `server/internal/httpserver/static/favicon.ico` | — | `git rm` |
| `server/internal/httpserver/static/splash.png` | — | `git rm` |
| `server/internal/httpserver/static/Helvetica-Black.woff` | — | `git rm` |
| `server/internal/httpserver/static/Helvetica-Black.woff2` | — | `git rm` |
| `server/internal/httpserver/static/` (empty dir) | — | removed by git when last file is rm'd |
| `CNAME` (root) | — | `git rm` (GitHub Pages custom-domain artifact) |
| `server/tools/copy-static.sh` | — | `git rm` |
| `server/internal/httpserver/static.go` | — | `git rm` (replaced by the new package import) |

**Practical sequence for the moves** (so git history is clean):

```
# Stage 1: drop the duplicates so the repo-root files can move into place.
git rm server/internal/httpserver/static/index.html \
       server/internal/httpserver/static/style.css \
       server/internal/httpserver/static/script.js \
       server/internal/httpserver/static/favicon.ico \
       server/internal/httpserver/static/splash.png \
       server/internal/httpserver/static/Helvetica-Black.woff \
       server/internal/httpserver/static/Helvetica-Black.woff2

# Stage 2: move the canonical copies.
mkdir -p server/public
git mv index.html              server/public/index.html
git mv style.css               server/public/style.css
git mv script.js               server/public/script.js
git mv favicon.ico             server/public/favicon.ico
git mv splash.png              server/public/splash.png
git mv Helvetica-Black.woff    server/public/Helvetica-Black.woff
git mv Helvetica-Black.woff2   server/public/Helvetica-Black.woff2
```

Note: `git mv A B` after `git rm` of a *different* file at B (different filename, same content) effectively records a rename when content matches and the staged add+remove pair is detected. Both copies were verified-identical via the existing `copy-static.sh` workflow, so git's rename detection should pick them up cleanly. If not, the diff is still semantically correct — only `git log --follow` aesthetics differ.

## New package: `server/public/embed.go`

```go
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
```

Notes:
- No `fs.Sub("public")` indirection — the embed lives next to the files, so the FS root is already the "public" view.
- Patterns enumerated by extension to keep the manifest auditable (mirrors the spirit of the old `copy-static.sh` explicit FILES list, but checked at compile time).
- Package name `public` — no naming clash; httpserver imports it as `public.FS()`.

## `server/internal/httpserver/static.go` — delete

The file is replaced by the new package import. The `//go:generate` directive tied to `copy-static.sh` goes with it. No `staticFS`/`StaticFS()` symbol survives.

## `server/internal/httpserver/router.go` — update

Before:
```go
staticFS, err := StaticFS()
if err != nil {
    return nil, err
}
staticHandler := http.FileServer(http.FS(staticFS))
```

After:
```go
import "github.com/forrestalmasi/thankyou/server/public"
...
publicFS := public.FS()
staticHandler := http.FileServer(http.FS(publicFS))
```

The empty-embed sanity check (`fs.ReadDir(publicFS, ".")` returns 0 entries → `errEmbedEmpty`) **stays** — it now catches a corrupted build rather than a forgotten copy-static run, but the safety value is the same. Update its error string:

```go
func (e *embedError) Error() string {
    return "httpserver: embedded public FS is empty (corrupted build?)"
}
```

The variable name `staticFS` in router.go can stay (renaming to `publicFS` is preference, not requirement) — the package boundary is what changed.

## Renderer path safety

**No renderer changes.** Verified: `server/internal/render/template.go` embeds its own sibling `server/internal/render/Helvetica-Black.woff` independently of any `static/` tree. The render package keeps its private font copy unchanged. The repo will then have exactly two physical copies of the WOFF file:

- `server/public/Helvetica-Black.woff` — served to browsers via the embedded static FS.
- `server/internal/render/Helvetica-Black.woff` — fed into resvg's font database server-side.

This duplication is intentional and pre-existed this task. (Collapsing those two into one is feasible — the render package would import `…/server/public` and read the asset via the embed — but it's out of scope for TYB-14 and adds an undesired httpserver↔render coupling. Leave it.)

## README.md changes

| Section | Current | After |
|---|---|---|
| Intro paragraph (L3) | "GitHub Pages site for..." | "Site for generating text in the classic 'THANK YOU' plastic bag style. A Go server in [server/](server/) renders the static site and print-quality PNGs from the same SVG template." (drop the GH Pages framing; thankyoubag.online callout stays on L5.) |
| "Run the server locally" intro (L9) | unchanged | unchanged |
| Step 1 (L11) | "Clone the repo, then `cd server`." | unchanged |
| Step 2 (L13-19) — copy-static block | full block | **deleted entirely**; renumber subsequent steps. |
| Step 3 → Step 2 (env vars) | renumbered | unchanged content |
| Step 4 → Step 3 (verify) | renumbered | unchanged content |
| Architecture "Two pieces today" (L107) | "The static site at the repo root is the GitHub Pages deploy..." | Replace with: "**One piece.** The Go server in [server/](server/) embeds and serves the static site (HTML/CSS/JS/font/images) from [server/public/](server/public/) and exposes the render API. Single binary, single deploy target." |
| "What's deferred" (L148) | "...and the deploy + DNS cutover from GitHub Pages to the Go server." | drop that final phrase. (DNS cutover is now an operator task that lives outside Lattice.) |
| Repo layout (L150-167) | two-bucket layout: GH Pages root + server/ | single bucket. Drop the entire "GitHub Pages site sits at the repo root" paragraph (L152-156). The Go-server bullets (L158-167) stay, with these edits:  drop the `tools/copy-static.sh` bullet (L166); add a bullet for `server/public/` — "static-site assets baked into the binary via `//go:embed`; HTML/CSS/JS plus the WOFF fonts and splash/favicon."; the `internal/httpserver/` bullet's "embedded-static FS" phrase becomes "router and handlers; static fall-through served from [server/public/](server/public/)."; remove the CNAME and `splash.png` mentions in any old "repo root" line — they live at `server/public/` now. |

## CLAUDE.md / agents.md changes

**None required.** Both files were grepped for "GitHub Pages", "github.io", "copy-static" — no hits. The dual-deploy story isn't part of the agent-facing convention surface.

## `.claude/settings.local.json`

Three lines reference `copy-static.sh` (lines 28-29, 57). They're auto-allow entries for the now-deleted script. Leave them as-is in this PR — they're harmless dead permissions and `update-config` cleanup isn't part of the task scope. (Future-proof: a follow-up can drop them; not blocking.)

## Order of operations (commit-by-commit)

The goal is: never have a broken build at any commit boundary.

**Commit 1 — "TYB-14: move static assets to server/public, update embed":**

1. `mkdir -p server/public` (cannot use `git mkdir`; Stage 2 below records it via the first `git mv`).
2. Run the Stage 1 + Stage 2 git commands listed in "Concrete file moves" above.
3. Create `server/public/embed.go` with the package shown above.
4. Edit `server/internal/httpserver/router.go`: import the new package, replace the `StaticFS()` call with `public.FS()`, update the empty-embed error string.
5. `git rm server/internal/httpserver/static.go`.
6. `cd server && go build ./...` — must succeed.
7. `cd server && go test ./...` — must pass (including golden render tests).
8. `git add` the new files; commit. **At this commit, the build is green and the server serves from `server/public/`.**

**Commit 2 — "TYB-14: drop copy-static.sh and update README":**

1. `git rm server/tools/copy-static.sh`.
2. Rewrite `README.md` per the table above.
3. Verify no leftover references to copy-static anywhere except `.lattice/` history (intentionally untouched) and `.claude/settings.local.json` (intentionally untouched).
4. Commit.

**Commit 3 — "TYB-14: drop GitHub Pages CNAME":**

1. `git rm CNAME`.
2. Commit. (Separate commit because deleting CNAME is the cleanest single-line "we are no longer a GitHub Pages site" marker. Kept atomic so a hypothetical revert of the GH Pages decision touches one file.)

If any `.github/workflows/*.yml` ever existed, this commit would also delete them — verified absent (`ls .github/` returned no such directory).

## Verification steps

After all three commits:

1. **Build:** `cd /Users/forrest/Code/thankyou/server && go build ./...` — exits 0.
2. **Tests:** `cd /Users/forrest/Code/thankyou/server && go test ./...` — all green, especially the golden-PNG render test (uncovers a renderer-WOFF regression if one slipped in).
3. **Vet:** `cd /Users/forrest/Code/thankyou/server && go vet ./...` — no diagnostics.
4. **Run the server:** `cd /Users/forrest/Code/thankyou/server && ./tools/run-dev.sh` (in another terminal). Then curl-smoke the static fall-through:
   - `curl -fsS http://localhost:8080/` — returns `<!DOCTYPE html>...` (index.html bytes).
   - `curl -fsS http://localhost:8080/style.css | head -5` — returns CSS.
   - `curl -fsS -o /tmp/got.woff2 http://localhost:8080/Helvetica-Black.woff2 && file /tmp/got.woff2` — file reports WOFF2 / 23624 bytes.
   - `curl -fsS http://localhost:8080/healthz` — returns `ok`.
   - `curl -fsS -X POST -H 'Content-Type: application/json' -d '{"text":"FOO","middletext":"BAR"}' http://localhost:8080/api/render` — returns `{"file_id":"...","url":"..."}`. (Confirms render path still works — i.e., the renderer's independent WOFF embed survived.)
5. **Repo cleanliness:**
   - `ls /Users/forrest/Code/thankyou/` — no .html/.css/.js/.png/.ico/.woff/.woff2 files at the repo root, and no CNAME.
   - `ls /Users/forrest/Code/thankyou/server/internal/httpserver/static/` — directory does not exist.
   - `ls /Users/forrest/Code/thankyou/server/tools/` — only `run-dev.sh` remains.
   - `find /Users/forrest/Code/thankyou -type d -name .github` — no result.
6. **Index byte-equality** (sanity guard against accidental edit during the move): `git show HEAD:server/public/index.html | sha256sum` should equal the pre-refactor `git show <pre-refactor-HEAD>:index.html | sha256sum`. (Use `git log --all -- index.html` to find the pre-move commit if needed.)

## Acceptance criteria

- [ ] No file with extensions `.html`, `.css`, `.js`, `.png`, `.ico`, `.woff`, `.woff2` exists at the repo root.
- [ ] No `CNAME` file at the repo root.
- [ ] No `.github/` directory.
- [ ] `server/internal/httpserver/static/` does not exist.
- [ ] `server/internal/httpserver/static.go` does not exist.
- [ ] `server/tools/copy-static.sh` does not exist.
- [ ] `server/public/` exists and contains exactly: `embed.go`, `index.html`, `style.css`, `script.js`, `favicon.ico`, `splash.png`, `Helvetica-Black.woff`, `Helvetica-Black.woff2` (8 files).
- [ ] `server/internal/render/Helvetica-Black.woff` is unchanged (renderer's private copy).
- [ ] `cd server && go build ./...` exits 0.
- [ ] `cd server && go test ./...` passes (including golden-render test).
- [ ] `cd server && go vet ./...` clean.
- [ ] `curl http://localhost:8080/` returns the same `index.html` bytes as the pre-refactor file (sha256 equality, modulo whitespace if any was reformatted — none should be).
- [ ] `curl http://localhost:8080/style.css`, `/script.js`, `/Helvetica-Black.woff2`, `/favicon.ico`, `/splash.png` all return 200 with the correct content type.
- [ ] README.md has no remaining references to `copy-static.sh`, "GitHub Pages", or the dual-deploy "two pieces" framing.
- [ ] CLAUDE.md and agents.md unchanged.

## Out of scope (explicit reminders)

- **DNS.** The CNAME file is a GitHub Pages artifact and goes; the actual `thankyoubag.online` DNS record cutover to a new host is the operator's job in a follow-up task. No DNS changes happen in this PR.
- **Hosting platform.** No decisions about Fly/Render/Railway/Cloud Run/etc. The plan does not touch deploy infra.
- **`.claude/settings.local.json` cleanup.** The two `copy-static.sh` permission entries become dead but are harmless. Leave them; a future agent can prune.
- **Render-package WOFF deduplication.** Out of scope. The two physical WOFF files (one in `server/public/`, one in `server/internal/render/`) stay duplicated for now to avoid coupling the render package to httpserver's asset path.
- **Lattice plan history.** The reference to `copy-static.sh` in `.lattice/plans/task_01KQVA5XQC3AB6S0YP449GNARY.md` is left as-is. Lattice plan files are immutable historical record.

## Critical files for implementation

- /Users/forrest/Code/thankyou/server/internal/httpserver/static.go (delete)
- /Users/forrest/Code/thankyou/server/internal/httpserver/router.go (edit: swap import + call site)
- /Users/forrest/Code/thankyou/server/public/embed.go (new file)
- /Users/forrest/Code/thankyou/server/tools/copy-static.sh (delete)
- /Users/forrest/Code/thankyou/README.md (edit per table)

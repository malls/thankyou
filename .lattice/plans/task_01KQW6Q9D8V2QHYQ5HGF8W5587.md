# TYB-10: set up dot env for local testing.

env vars should be stored in .env, with examples in .env.example

## Scope

Files to create:

- `/.env.example` — tracked; documents every env var the server reads, with placeholder values and inline `#` comments. Mirrors the keys block in `server/cmd/server/main.go` and `README.md`.
- `/server/tools/run-dev.sh` — tracked; small wrapper that sources `../.env` (repo-root) if present, then `exec`s `go run ./cmd/server`. Idempotent, `set -euo pipefail`, uses `set -a; source ...; set +a` so all vars are exported without manual `export` per line.

Files to modify:

- `/.gitignore` — currently empty/tracked. Add `.env` and `.env.*` (with a `!.env.example` allowlist) to belt-and-brace the existing global ignore. Self-documenting for anyone who clones without our global gitignore.
- `/README.md` — update "Run the server locally" step 3 to instruct `cp .env.example .env`, edit, then `./tools/run-dev.sh` (or `set -a; source ../.env; set +a; go run ./cmd/server`). Keep the env-var list (descriptions stay) but cross-reference `.env.example` as the source of truth for the *keys*.
- `/CLAUDE.md` and `/agents.md` — no changes required (they're about Lattice workflow, not project run steps). Skip.

Files explicitly **not** modified:

- `/.env` — already exists at repo root (0 bytes, untracked, ignored by global gitignore). Leave it alone; the user populates it themselves from `.env.example`.
- Any Go source — no code change. We're not adding `godotenv` (rationale below).

## Inventory

Every env var the codebase reads, all in `server/cmd/server/main.go`:

| Key                  | Read at                                | Required?                                                                 | Default          | Purpose |
|----------------------|----------------------------------------|---------------------------------------------------------------------------|------------------|---------|
| `PORT`               | `main.go:40` (via `envOr`)             | Optional                                                                  | `8080`           | HTTP listen port. |
| `DATA_DIR`           | `main.go:41` (via `envOr`)             | Optional                                                                  | `./data/files`   | Where rendered PNGs are written. |
| `PRINTFUL_TOKEN`     | `main.go:131`                          | Optional for boot; **required** for `/api/printful/*` (else routes 503).  | (unset)          | Printful API bearer token. |
| `PRINTFUL_STORE_ID`  | `main.go:136`                          | Optional (only with account-level tokens; store-level tokens leave empty) | (unset)          | `X-PF-Store-Id` header. |
| `PUBLIC_BASE_URL`    | `main.go:137`                          | Optional, but warned-at-boot when `PRINTFUL_TOKEN` is set and this isn't  | (unset)          | Absolute URL Printful uses to GET the print PNG (e.g. ngrok tunnel). |

Client side: zero env reads. `script.js` and `index.html` use no `process.env` / `import.meta.env` — the static site is served as-is. So `.env.example` only needs to cover the server.

## Approach

### Loading mechanism: shell-source, not godotenv

Three options were considered:

1. **Add `github.com/joho/godotenv`.** Rejected. Pulls in a transitive dep for ~30 lines of "if file exists, parse it." Promotes a pattern where prod and dev load the same way, which is the wrong shape for prod (prod env should come from the platform/secrets manager, not a checked-in file). go.mod stays lean.

2. **Source `.env` from a shell wrapper.** Chosen. Simple, transparent, works with any shell; the binary stays env-pure (no surprise file reads at boot); prod is unaffected; matches how a human runs `PRINTFUL_TOKEN=… go run …` today, just hoisted into a file. The wrapper is `server/tools/run-dev.sh`:

   ```bash
   #!/usr/bin/env bash
   set -euo pipefail
   SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
   REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
   ENV_FILE="$REPO_ROOT/.env"
   if [[ -f "$ENV_FILE" ]]; then
     set -a
     # shellcheck disable=SC1090
     source "$ENV_FILE"
     set +a
   fi
   cd "$SCRIPT_DIR/.."
   exec go run ./cmd/server "$@"
   ```

3. **"Document it, the user does it themselves."** Rejected as the *only* path — that's what we have today and it's the friction this task is fixing. But the README will still mention the manual `set -a; source .env; set +a` form for users who don't want the wrapper.

`.env` lives at the **repo root**, not under `server/`, because some env vars (`DATA_DIR`, `PUBLIC_BASE_URL`) describe runtime behavior of *this checkout* and feel naturally repo-scoped, and it's where most engineers look first. The wrapper resolves it as `<repo>/.env` so working directory doesn't matter.

### `.env.example` shape

```
# Server config (all optional; defaults shown).
PORT=8080
DATA_DIR=./data/files

# Printful integration. Leave PRINTFUL_TOKEN unset to run the server without
# the /api/printful/* routes (they'll return 503 with the saved file_id+
# file_url so the rest of the UI degrades gracefully).
PRINTFUL_TOKEN=
# Account-level tokens only. Store-level tokens: leave empty.
PRINTFUL_STORE_ID=
# Absolute URL Printful will GET the print PNG from. Use your ngrok/cloudflared
# tunnel URL when running locally, your public hostname when deployed. Empty
# falls back to the inbound Host header (works for tunneled localhost only).
PUBLIC_BASE_URL=
```

Comments inline; values left blank for the secrets so a careless `cp -n` over an existing `.env` is recoverable.

## Acceptance criteria

- `/.env.example` exists at the repo root, is tracked by git, lists all five env vars (`PORT`, `DATA_DIR`, `PRINTFUL_TOKEN`, `PRINTFUL_STORE_ID`, `PUBLIC_BASE_URL`) with brief inline comments and matching default values where applicable.
- `/.env` is **not** tracked. `git check-ignore .env` returns a positive match (already true via global gitignore; the repo-root `.gitignore` rule makes it explicit for clones without our global config).
- `/.gitignore` ignores `.env` and `.env.*` while allowlisting `.env.example` (`!.env.example`). `git status` on a populated `.env` shows nothing.
- `/server/tools/run-dev.sh` exists, is `chmod +x`, runs the server with vars sourced from `<repo>/.env` when present, and works when `.env` is absent (skips sourcing, uses defaults / process env).
- `README.md` "Run the server locally" step 3 documents the new flow: `cp .env.example .env` → edit → `./tools/run-dev.sh` (with the manual `set -a; source ../.env; set +a; go run ./cmd/server` form as an alternative). The existing env-var list survives.
- A fresh-clone walkthrough succeeds: clone → `cd server` → `./tools/copy-static.sh` → `cp ../.env.example ../.env` → edit `PRINTFUL_TOKEN` → `./tools/run-dev.sh` → server boots, logs the `printful integration enabled` line.
- Running with no `.env` still boots the server with defaults and the existing "PRINTFUL_TOKEN unset" warning.
- Existing tests still pass (`go test ./...` from `server/`). No code change means no test change expected.

## Out of scope

- **Production secrets management.** No changes to deploy / hosting config; prod will continue to receive env vars from the platform (whatever it ends up being), not a checked-in file. `.env` is a local-dev convenience only.
- **Per-environment overrides** (`.env.test`, `.env.production`, `.env.local`). Single `.env` is enough for one developer on one machine. We'll add layering only when there's a real second environment.
- **Adding `godotenv` to the Go binary.** See "Approach" rationale; can be revisited if shell-sourcing turns out to be a friction point but unlikely.
- **Client-side env vars.** None exist today; the static site has no build step. If a build step lands later (Vite/Next/etc.), that tooling reads `.env*` natively and we'll extend `.env.example` with `VITE_…` keys at that point.
- **Validating env-var shape at boot** (e.g., URL parses, port is numeric). The handlers already degrade gracefully on missing/wrong values; tightening this is a separate task.
- **Updating the existing 0-byte repo-root `.env` file.** It's the user's local file; we don't write to it.

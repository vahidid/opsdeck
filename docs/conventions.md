# Coding conventions

This is the baseline that new code and reviews are held to. It writes down what the
code already does (counts measured on `main` at 1.0.2), and makes a call where two
styles coexist. Existing code is not rewritten to match; code you touch should.

Rules are marked **must** (a review blocks on it) or **should** (a review asks why
not). Everything else: match the code around your change, as
[CONTRIBUTING.md](../CONTRIBUTING.md) says.

## Checks before a pull request

| Check | Command | Status on `main` |
|---|---|---|
| Go format | `gofmt -l cmd internal tools` prints nothing | **must** — clean (CI does not check it) |
| Go vet | `go vet ./...` | **must** — clean, CI runs it |
| Go tests | `go test ./...` | **must** — CI runs it |
| TypeScript | `cd web && npm run typecheck` | **must** — clean, CI runs it |
| Browser tests | `cd e2e && npx playwright test` | **must** for UI changes — CI runs it |
| staticcheck | `go run honnef.co/go/tools/cmd/staticcheck@latest ./...` | **should** — one documented exception (known debt); add none |

## Go

### Layout

- `cmd/` holds thin binaries: `cmd/opsdeck` (stdlib `flag`, a `switch` on the
  subcommand, no CLI framework) and `cmd/opsdeck-desktop` (Wails).
- `internal/` is flat: one package per domain, no subpackages (only `testutil/*`).
  A new domain gets a new package and a line in [architecture.md](architecture.md).
- A package that wraps a library or tool of the same name takes an `x` suffix
  (`gitx`, `dbx`, `acmex`, `mcpx`) so imports don't clash.
- Each package has a file named after itself (`dbx/dbx.go`) holding the package doc.
- File names are lowercase words run together: `sessionops.go`, not `session_ops.go`.
  Underscores only for `_test` and OS suffixes (`_windows.go`, `_other.go`).
- A domain's main type is usually `Manager`, built with `New`.

### Errors

- **must** Error strings start lowercase (except proper nouns: "Anthropic", "Ansible")
  and have no trailing period. staticcheck ST1005 enforces the capitalisation, so a
  message may not even begin with a proper noun — rephrase it ("the Anthropic API …").
- **must** Wrap an underlying error with `%w`, not `%v`/`%s`, so `errors.Is`/`As` work:
  `fmt.Errorf("parse docker compose ls: %w", err)`. Today only 117 of 782 `Errorf`
  calls do; new code always does.
- **should** Write messages for the user and say what to do next:
  `"the session is already working; wait or interrupt it"`.
- **must** No `panic`. `log.Fatal` and `os.Exit` only in `cmd/`.
- **must** An error the HTTP layer maps to a status is a sentinel (`ErrNotFound`,
  `ErrConflict`) checked with `errors.Is`. Do not add new
  `strings.Contains(err.Error(), …)` cases to `server.fail`.
- A custom error type that carries a cause **should** implement `Unwrap`.

### Logging and audit

- Stdlib `log.Printf`, plain sentences, no structured logger. `cmd/` prefixes with
  `opsdeck: `.
- What a user or the agent *did* goes to the audit journal, not the log.
- Noisy library loggers are silenced where they are set up (`klog`, mysql).

### Context and concurrency

- **must** `ctx context.Context` is the first parameter of anything that does I/O.
  No `context.TODO`.
- **must** Every outbound call has a deadline (`context.WithTimeout`) or a caller's ctx.
- Handlers that start a long job detach it with `context.WithoutCancel(r.Context())`.
- Context keys are unexported empty structs (`type delegateKey struct{}`).
- Plain `go` + `sync.Mutex` (field named `mu`, `defer x.mu.Unlock()`), `WaitGroup`,
  channels. No errgroup.

### Processes, files, secrets

- **must** Start child processes with `sysproc.Command`/`CommandContext`, not
  `exec.Command`, so Windows desktop builds don't open console windows. Direct `exec`
  only in code that never runs on Windows.
- **must** File modes in `0o` form: `0o600`, not `0600`.
- **must** Config files hold no secrets; they hold `vault://scope/name` references.
- **must** Anything that can show a secret to the model, a log or the UI goes
  through `agent.Redactor`.
- **must** New agent tools go through risk classification, approval, redaction and
  audit ([harness.md](harness.md)).
- Write config by temp file + rename (`config.Store`).

### HTTP server (`internal/server`)

- Stdlib `http.ServeMux` with method patterns (`"GET /api/…/{name}"`, `r.PathValue`).
- Each domain registers its routes in an `xRoutes(mux, u)` function, wrapping
  handlers with `u` (refuses requests while the vault is locked).
- Handlers are `func (s *Server) x(w http.ResponseWriter, r *http.Request)`.
- **must** Use `readJSON` (16 MiB limit), `writeJSON`, `writeErr`, `fail` — no
  hand-rolled `json.NewEncoder(w)`.
- Request bodies are anonymous structs: `var body struct{ … }`.
- Status codes are numeric literals in `writeJSON(w, 200, …)` (287 uses vs 15
  constants); keep it that way for consistency.
- JSON tags camelCase for opsdeck's own API; snake_case only to match an external API.

### Style

- Receivers are one letter (`s`, `c`, `m`), pointer receivers by default.
- Initialisms fully capitalised: `HTTPURL`, `ToolUseID`.
- Dependencies are injected as func fields on a struct (`agent.Deps`); interfaces are
  rare and live next to their implementations (`gitx.Provider`).
- `any`, never `interface{}`.
- **should** New code uses `slices`/`maps`, not `sort` (91 uses today, 8 `slices`).
- Imports in three groups: stdlib, third-party, `opsdeck/internal/…`.
- Exported identifiers have a doc comment that starts with the name: "New returns a
  manager." Plain full sentences, `// -----` section dividers. No TODO/FIXME, no
  `nolint`, no `/* */`.
- **must** No unused code (staticcheck U1000 is clean on `main`).
- Dependencies: stdlib first; a maintained native Go library before shelling out
  ([decisions.md](decisions.md)); plain `net/http` for small REST APIs.

### Tests

- **must** Stdlib `testing` only: no testify, go-cmp, gomock.
- **must** Fakes, not mocks: real in-process servers from `internal/testutil`
  (`fakellm`, `sshd`, `gitserver`, …) or `httptest`.
- Tests needing outside services skip when their env var is unset
  (`OPSDECK_TEST_POSTGRES`, `OPSDECK_VPN_LAB`, …), and CI sets the database ones.
- Table tests as inline `for _, c := range []struct{…}` loops; `t.Run` when cases need
  names.
- Use `t.TempDir`, `t.Setenv`, `t.Helper`. No `t.Parallel` (tests share env vars).
- Tests sit in the same package as the code.
- **must** Behaviour you change has a test (CONTRIBUTING.md).

## Web UI (`web/`)

### Layout

- `api/` — `client.ts` (the one `api` object, every endpoint) and `types.ts`.
- `components/` — shared widgets; `ui.tsx` and `overlay.tsx` hold the primitives.
- `context/` — app-wide providers; `hooks/index.ts` — generic hooks; `lib/` — pure
  helpers, no React.
- `pages/` — one page per file (`XPage`), or a feature folder with `index.tsx` and a
  `shared.tsx` for the feature's context, hooks and small widgets.
- File case: PascalCase for a file that exports one component (`LogViewer.tsx`);
  lowercase for a module with many exports (`ui.tsx`, `shared.tsx`, all of `lib/`
  and `context/`). No kebab-case.
- **should** Keep new files under ~600 lines; split a page into a feature folder
  before it grows past that (`pages/git/`, `pages/topology/` and the agent feature
  folder show the pattern).

### TypeScript and components

- `strict`, `noUnusedLocals`, `noUnusedParameters` are on; **must** stay on.
- **must** No new `any`, no `@ts-ignore`. Prefer narrowing over `!`.
- Components are `export function Name(…)` — named exports, no default exports
  (except a lazy-loaded module), no `React.FC`, no arrow-const components.
- Props typed inline and destructured: `function Foo({ a, b }: { a: string; b?: number })`.
  A named `XProps` type only when it's shared.
- `type`, never `interface`. No `enum`; use string unions.
- Imports are relative (no `@/` alias); `import type { … }` on its own line.

### Data and state

- **must** All server calls go through `api.x()` in `api/client.ts`; add the endpoint
  there. No `fetch` in pages.
- Reads: `const r = useAsync(() => api.x(…), [deps])` with
  `<ErrorBox error={r.error} onRetry={…} />` and `<Loading />`.
- Writes: local `busy` state, `toast.error(errMsg(e))` on failure,
  `await confirm({…})` before anything destructive.
- **should** Use `useLocalStorage`, not `localStorage` directly.
- Fire-and-forget promises are marked `void`.
- Context providers wrap their value in `useMemo`; their hook throws when the
  provider is missing.
- Live data: `EventSource` (SSE) inside `useEffect` with a cleanup; WebSockets only
  for terminals.
- `// eslint-disable-next-line react-hooks/exhaustive-deps` comments exist (35) but
  no ESLint runs; don't add more — fix the deps or explain in a comment.

### Styling

- **must** Theme tokens (`bg-panel-2`, `text-muted`, `border-line`), not raw Tailwind
  colours, and no `dark:` — themes switch through the CSS variables in `index.css`.
- Use the primitives in `components/ui.tsx` and `overlay.tsx` (Button, Input, Modal,
  Drawer, Table…) before writing a new one.
- Merge classes with `cx()` from `ui.tsx`.
- Icons from `lucide-react`.
- Text that can be Persian (agent output, markdown, user input) gets
  `dir={textDir(…)}` from `lib/rtl.ts`; inside it use logical classes (`ms-`, `ps-`).

### Formatting

No formatter runs, so match this by hand: double quotes, semicolons, 2-space indent,
trailing commas on multi-line lists. Imports roughly: react, router, lucide,
`api`, context, components, hooks, lib, then `./`.

### Browser tests (`e2e/`)

- Selectors are accessibility-first: `getByRole`, `getByText`, `getByLabel`.
  **must** No `data-testid`; a `locator()` only for things with no role.
- Flat `test("sentence title", …)` calls, using `open(page, path)` from
  `tests/fixture.ts`.
- One worker; the server comes from `tools/e2e` (local SSH servers, fake model).

## Docs and changelog

- **must** A user-visible change adds a line under *Unreleased* in
  [CHANGELOG.md](../CHANGELOG.md) and updates `docs/`.
- Comments and docs are plain English, full sentences, and say why.

## Review checklist

1. Checks table passes; no new staticcheck findings.
2. One change per pull request, title says what and why.
3. Errors wrapped with `%w`, lowercase, actionable; HTTP mapping via sentinels.
4. Child processes via `sysproc`; secrets via vault refs and `Redactor`.
5. New agent tool: risk class, approval, redaction, audit.
6. Server calls via `api` client; theme tokens; UI primitives reused.
7. Tests with fakes from `testutil`; e2e for UI flows.
8. CHANGELOG and docs updated for user-visible changes.

## Known debt on `main`

Fix these when you touch the file; don't open a pull request just for them.

- staticcheck: 1× SA1019 (deprecated `k8s.io/apimachinery/pkg/util/httpstream` in
  `kube/exec.go`). `client-go`'s `spdy` dialer and `portforward` still speak the
  old types, so the move to `k8s.io/streaming/pkg/httpstream` waits for client-go;
  revisit on the next client-go bump.
- No CI check for `gofmt` or staticcheck, so lint debt drifts.

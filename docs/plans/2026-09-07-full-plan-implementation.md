# MetaTube Full Plan Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use `superpowers:executing-plans` to implement this plan task-by-task.

**Goal:** Complete the planned Emby-compatible media-server core, management API, admin UI, and automated interface acceptance coverage.

**Architecture:** Keep the current single-binary Go/Gin design. Strengthen `store`, `scanner`, and `server` around a consistent source-of-truth flow: NFO/files → SQLite index → versioned caches → Emby/admin APIs. Add only the scheduler state and UI behaviour required by `dev-plan.md`.

**Tech Stack:** Go 1.25, Gin, modernc SQLite, native embedded HTML/CSS/JS, Go `testing`/`httptest`.

---

### Task 1: Repair scan and media index consistency

**Files:**
- Modify: `internal/scanner/scanner.go`
- Modify: `internal/store/store.go`
- Modify: `internal/imageutil/webp.go`
- Test: `internal/server/server_test.go`

1. Add tests covering image URLs after scanning, final source lines without a trailing newline, deleted source reconciliation, and unplayed filtering.
2. Run targeted tests and confirm they fail for the current defects.
3. Resolve image paths before upserting a scanned movie; retain the user-approved WebP-only policy.
4. Treat `io.EOF` after a valid first source line as success.
5. Reconcile scanned paths for each library and delete stale database entries.
6. Use a `NOT EXISTS` userdata clause for IsUnplayed.
7. Run `gofmt`, `go vet ./...`, and `go test ./...`.

### Task 2: Complete management data operations

**Files:**
- Modify: `internal/store/store.go`
- Modify: `internal/server/server.go`
- Modify: `internal/nfo/nfo.go`
- Test: `internal/server/server_test.go`

1. Add reindex, delete, paginated/filterable admin items, and complete reread state transitions.
2. Make manual/edit flows persist all supported NFO fields and image paths.
3. Use store version bumps on every movie/userdata write; cache keys retain versioned list semantics.
4. Add tests for reindex idempotence, incompatible→HTTP reread, admin pagination, deletion, and cache freshness.
5. Format and test.

### Task 3: Complete protocol and probe acceptance

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/store/store.go`
- Test: `internal/server/server_test.go`

1. Validate the single Emby user ID for user-scoped routes.
2. Return consistent 404/415 errors for missing and incompatible sources.
3. Validate item existence for progress and played APIs; invalidate caches after updates.
4. Bound and clear probe records; only record non-admin unknown routes.
5. Add an acceptance fixture with a local Range upstream, test GET/HEAD redirects and Range playback, systems, views, progress, played state, errors, and probes.
6. Format and test.

### Task 4: Make tasks and the admin UI functional

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/web/app.js`
- Modify: `internal/server/web/style.css`
- Test: `internal/server/server_test.go`

1. Replace static task status with scan task history.
2. Add UI controls for scan, reindex, edit/reread/delete, status guidance, and probe cleanup.
3. Escape all dynamic text before it reaches `innerHTML`.
4. Add focused responsive, reduced-motion, and empty/error states.
5. Run JavaScript syntax validation and Go tests.

### Task 5: Final validation

**Files:**
- Test: all Go packages

1. Run `gofmt -w` for changed Go files.
2. Run `go vet ./...`.
3. Run `go test ./...`.
4. Run `go build ./cmd/metatube`.
5. Report remaining deviations from `dev-plan.md`, if any.

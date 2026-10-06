# Mirror-GUI Test Documentation

All tests run automatically on every push and pull request via the GitHub Actions workflow `[.github/workflows/mirror-gui-tests.yml](.github/workflows/mirror-gui-tests.yml)`. The workflow contains five parallel jobs described below.

---

## CI Jobs Overview


| Job                 | Runner          | What it does                                                                         |
| ------------------- | --------------- | ------------------------------------------------------------------------------------ |
| **backend**         | `ubuntu-latest` | `gofmt` check, `go vet`, Go unit and API tests with the race detector and coverage    |
| **frontend**        | `ubuntu-latest` | Frontend build, ESLint, Vitest script tests                                           |
| **e2e**             | `ubuntu-latest` | Playwright end-to-end browser tests against the Go server serving the built frontend |
| **shellcheck**      | `ubuntu-latest` | Static analysis of all shell scripts                                                 |
| **container-image** | `ubuntu-latest` | Validates the Dockerfile builds successfully with Podman                             |


---

## Job 1: backend

1. **Formatting** (`gofmt -l cmd internal`) -- fails if any Go file is not gofmt-formatted
2. **Vet** (`go vet ./...`)
3. **Unit and API tests** (`go test -race -cover ./...`)

### Go tests (`internal/server/`, `internal/catalogmeta/`)

Tests use only the standard library. API tests drive the real `http.Handler` with `net/http/httptest`; each test gets a fresh server with temporary storage and the catalog fixture in `tests/fixtures/catalog-data/`. Tests that run operations put a fake `oc-mirror` script first on `PATH`; catalog sync tests pull multi-arch catalog images from an in-memory OCI registry (`internal/catalogmeta/catalogtest`).


| File                                     | Description                                                                                                                                                       |
| ---------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/server/utils_test.go`          | Version parsing and sorting, catalog name resolution, channel extraction/normalization, path availability, optional oc-mirror flag validation, JSON-to-YAML conversion, catalog sync diff |
| `internal/server/api_test.go`            | Every REST endpoint: health, catalogs, operators/channels/versions/dependencies, config save/upload/download/delete, mirror folders, pull secret, registry verification (including the token flow against a local TLS registry), system info/status/paths, cache cleanup, operation list/stats/start/stop/delete/logs/details, SSE log streaming, oc-mirror success/failure/stop handling, catalog sync (missing pull secret and a full run against an in-memory registry), SPA serving, CORS |
| `internal/catalogmeta/catalogmeta_test.go` | FBC metadata generation compared against golden files produced by the former Python implementation (`testdata/`), version ordering, bundle-name versions, `catalog-info.json` / `catalog-index.json` output, auth-file keychain, `Sync` against an in-memory registry (linux/amd64 selection, digests, retries, partial failure) |


---

## Job 2: frontend

1. **Build** (`npm run build`) -- TypeScript compilation and Vite production build
2. **Lint** (`npm run lint`) -- ESLint on all `src/**/*.{ts,tsx}` files
3. **Script tests** (`npm run test`) -- Vitest run across `tests/scripts/`

### Script Tests (`tests/scripts/`)


| File                           | Tests | Description                                                                                                                                                                                                                                            |
| ------------------------------ | ----- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `auditFetchCatalogs.test.ts`   | 2     | Tests `scripts/audit-fetch-catalogs.mjs` logic using synthetic fixtures -- detects version metadata mismatches and JSON parse errors                                                                                                                   |
| `catalogDataIntegrity.test.ts` | 94    | Validates all committed catalog metadata: `catalog-index.json` has all 6 OCP versions and 3 catalog types, all 18 catalogs have valid `operators.json` (with required fields and minimum operator counts), `dependencies.json`, `catalog-info.json`, and optional `digest`/`synced_at` fields |
| `shellcheck.test.ts`           | 7     | Runs ShellCheck on shell scripts when available; skips gracefully otherwise                                                                                                                                                                            |


---

## Job 3: e2e

Runs Playwright browser tests using headless Chromium (port 3001 in CI against `go run ./cmd/mirror-gui` serving the built `dist/`, port 3000 locally against a running container).


| File                         | Tests | Description                                                                                                                    |
| ---------------------------- | ----- | ------------------------------------------------------------------------------------------------------------------------------ |
| `navigation.spec.ts`         | 12    | App loads, page title, sidebar nav items with correct routing, masthead title and version badge, sidebar toggle collapse/expand/persistence, dark theme class, logo image   |
| `dashboard.spec.ts`          | 4     | Dashboard shows environment overview, operation stats cards, recent operations section, quick action buttons                    |
| `mirrorConfig.spec.ts`       | 9     | Mirror Configuration page -- platform channels, operators (FieldBuilder + TypeaheadSelect), additional images, YAML preview, save/download, inline validation, digest toggle, load configuration   |
| `mirrorOperations.spec.ts`   | 8     | Mirror Operations page -- config file selector, start/run controls, operation table, filter dropdown with status options, select all checkbox, Delete All button |
| `history.spec.ts`            | 6     | History page -- title, filter controls, Export CSV button, filter dropdown with status options, select all checkbox, Delete All button |
| `settings.spec.ts`           | 5     | Settings page -- Pull Secret/Registry/Cache/Sync Catalogs tabs, key fields visible, sync and clear buttons                       |
| `configToOperations.spec.ts` | 1     | End-to-end workflow -- saves a YAML config via API, navigates to operations page, confirms it appears                          |
| `pullSecret.spec.ts`         | 6     | Pull secret -- Dashboard pull secret status, Environment Status label, popover, Pull Secret tab, URL tab navigation, status persistence   |


Playwright reports are uploaded as CI artifacts (retained 14 days).

---

## Job 4: shellcheck

Runs [ShellCheck](https://www.shellcheck.net/) with `-S error` (error-level severity) on all shell scripts:

- `mirror-gui.sh`
- `local-build.sh`
- `sync-catalogs.sh`

Scripts that are not present (e.g., gitignored) are skipped gracefully.

---

## Job 5: container-image

Builds the multi-stage Dockerfile with Podman to verify the container image builds successfully. Does not push to any registry. Has a 45-minute timeout to accommodate the oc-mirror binary download.

```
podman build -t mirror-gui:ci .
```

---

## Running Tests Locally

```bash
# Backend unit and API tests
go test ./...

# With race detector and coverage
go test -race -cover ./...

# Single backend test
go test ./internal/server -run TestOperationLogStream -v

# Script tests (Vitest)
npm test

# E2E tests (requires running container on port 3000)
npm run test:e2e

# E2E tests against a local Go server (builds nothing; run `npm run build` first)
CI=1 npm run test:e2e

# Lint
gofmt -l cmd internal && go vet ./...
npm run lint

# Audit-catalog script
npm run audit:fetch-catalogs
```

---

## Test Counts Summary


| Category           | Files  | Test Cases |
| ------------------ | ------ | ---------- |
| Go (unit + API)    | 3      | 49         |
| Scripts (Vitest)   | 3      | 10         |
| E2E (Playwright)   | 11     | 96         |
| **Total**          | **17** | **155**    |

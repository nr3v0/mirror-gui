## Project Overview

This is the `mirror-gui` repository — a web-based interface for managing OpenShift Container Platform mirroring operations using oc-mirror v2. It provides a visual configuration builder, operation execution with real-time monitoring, and environment management.

Mirror-GUI has two layers:
- **Frontend**: React 18 single-page application (TypeScript) using PatternFly 6, built with Vite
- **Backend**: Go HTTP server (standard library `net/http`) that wraps the oc-mirror v2 CLI, serves the REST API and the built frontend

Node.js is only needed to build and develop the frontend; the container image runs a single Go binary.

### How it works

The backend spawns `oc-mirror` as a child process to perform mirror-to-disk operations. Operator catalog metadata is pre-fetched at build time (via `sync-catalogs.sh`) and bundled into the container image, enabling offline browsing of operators, channels, and dependencies. The frontend communicates with the backend via a REST API, and live operation logs are streamed via Server-Sent Events (SSE).

### Key components

| Component | Location | Purpose |
|-----------|----------|---------|
| Server entrypoint | `cmd/mirror-gui/main.go` | Reads configuration from the environment and starts the HTTP server |
| Routes and middleware | `internal/server/server.go` | Configuration, route table, CORS, JSON helpers |
| Operations | `internal/server/operations.go` | Starting/stopping oc-mirror, operation records, logs, SSE log streaming |
| Catalog data | `internal/server/catalog.go` | Pre-fetched catalog metadata, operators, channels, versions, dependencies |
| Catalog sync API | `internal/server/catalogsync.go` | Runs the catalog sync in the background and reports progress/diff |
| Catalog metadata | `internal/catalogmeta/` | FBC parsing, metadata generation, `oc image extract` sync (`mirror-gui sync-catalogs`, `mirror-gui catalog-metadata`) |
| Configs | `internal/server/configs.go` | ImageSetConfiguration save/upload/download/delete and validation |
| Pull secret and registries | `internal/server/pullsecret.go` | Pull secret CRUD, registry authentication checks |
| System | `internal/server/system.go` | System info/health, mirror folders, cache cleanup, frontend serving |
| Server utilities | `internal/server/utils.go` | Version parsing, catalog name resolution, channel normalization, path availability |
| React app entrypoint | `src/App.tsx` | App shell with PatternFly layout, routing, theme/alert providers |
| Dashboard | `src/components/Dashboard.tsx` | System overview, stats, recent operations |
| Mirror Configuration | `src/components/MirrorConfig.tsx` | Visual ImageSetConfiguration builder |
| Mirror Operations | `src/components/MirrorOperations.tsx` | Operation execution and monitoring |
| History | `src/components/History.tsx` | Past operation review and CSV export |
| Settings | `src/components/Settings.tsx` | Pull secret, registry, cache, and catalog sync management |
| Catalog sync script | `sync-catalogs.sh` | Host wrapper that builds and runs `mirror-gui sync-catalogs` |
| Local build script | `local-build.sh` | Container build and run orchestration |

### Local container workflow

```bash
podman build -t localhost/mirror-gui .
IMAGE_NAME=localhost/mirror-gui ./mirror-gui.sh
```

`local-build.sh` wraps both steps; the manual path above is useful when iterating on the image or passing custom flags.

## Common Development Commands

### Setup

Requires Go (see `go.mod`) and Node.js 22 (frontend only).

```bash
npm ci                 # install frontend dependencies
npm run dev:server     # start the Go backend on port 3001 (go run ./cmd/mirror-gui)
npm run dev            # in a second terminal: Vite dev server with HMR on port 3000, proxying /api to 3001
```

### Building

```bash
npm run build                        # TypeScript check + Vite production build into dist/
go build -o mirror-gui ./cmd/mirror-gui   # backend binary; serves dist/ from its working directory
```

### Testing

```bash
go test ./...         # backend unit and API tests
npm test              # script tests (Vitest)
npm run test:e2e      # end-to-end tests (Playwright)
```

### Linting

```bash
gofmt -l cmd internal && go vet ./...   # backend
npm run lint                            # frontend ESLint check
npm run lint:fix                        # frontend ESLint auto-fix
```

## Contributing

1. Write understandable code. Always prefer clarity over other things.
2. Write comments and documentation in English.
3. Write tests for your code — unit/integration tests for backend, E2E tests for UI workflows.
4. When instructed to fix tests, do not remove or modify existing tests.
5. Run `go vet ./...`, `gofmt` and `npm run lint` before committing files.
6. Follow existing PatternFly patterns for UI components.
7. API changes should be reflected in [API.md](API.md).

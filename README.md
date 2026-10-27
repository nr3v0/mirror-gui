# Mirror-GUI

Mirror-GUI is a web-based interface for managing OpenShift Container Platform (OCP) mirroring operations using oc-mirror v2. It provides a visual configuration builder, operation execution with real-time monitoring, and environment management - without requiring command-line expertise.

The application runs as a containerized service (Podman) and wraps oc-mirror v2 to perform mirror-to-disk workflows. It uses pre-fetched operator catalog metadata to enable offline-capable operator browsing, channel selection, and dependency detection.

## Table of Contents

- [Mirror-GUI](#mirror-gui)
  - [Table of Contents](#table-of-contents)
  - [Getting Started](#getting-started)
    - [Prerequisites](#prerequisites)
    - [Clone the repository](#clone-the-repository)
    - [Build and run](#build-and-run)
  - [Features](#features)
    - [Dashboard](#dashboard)
    - [Mirror Configuration](#mirror-configuration)
    - [Mirror Operations](#mirror-operations)
    - [History](#history)
    - [Settings](#settings)
  - [Environment Variables](#environment-variables)
  - [Compatibility](#compatibility)
  - [Troubleshooting](#troubleshooting)
  - [API](#api)
  - [Testing](#testing)
  - [Development](#development)
    - [Architecture](#architecture)
  - [Contributing](#contributing)
  - [License](#license)

## Getting Started

### Prerequisites

- **Podman** (5.0+)
- **Go** (version in `go.mod`) — `sync-catalogs.sh` builds and runs the catalog sync tool (`mirror-gui sync-catalogs`)
- **Pull secret** from [console.redhat.com](https://console.redhat.com/openshift/downloads#tool-pull-secret) - save to `pull-secret/pull-secret.json` before building, or run `podman login registry.redhat.io`

### Clone the repository

```bash
git clone https://github.com/openshift/mirror-gui.git
cd mirror-gui
```

### Build and run

```bash
# Build and run locally (fetches catalogs, builds image, starts container)
./local-build.sh

# Build only, without starting the container
./local-build.sh --build

# Run a previously built image without rebuilding or fetching catalogs
./local-build.sh --run
```

By default the image compiles `oc-mirror` from source (the same upstream commit as Red Hat's newest build) with the current Go toolchain and patched Go modules, which clears the vulnerabilities reported in Red Hat's binary and keeps the image near 100 MB. To ship Red Hat's published binary instead, use `./local-build.sh --oc-mirror redhat` or `podman build --build-arg OC_MIRROR_FROM=redhat .`. Building from source needs network access to GitHub and the Go module proxy; use `redhat` for hermetic (offline/Konflux) builds.

### Multi-arch release images

```bash
# Build a manifest list for linux/amd64 and linux/arm64 and push it to quay.io
./release.sh --push 5.2.0
```

`release.sh` cross-compiles every architecture on the build host (no QEMU needed) and tags the manifest list `vVERSION`, the short git commit and `latest` (`--no-latest` skips `latest`). Use `--platforms linux/amd64,linux/arm64,linux/ppc64le,linux/s390x` for more architectures and `--image` for another repository. Without `--push` it only builds. Pulling `quay.io/nr3v0/mirror-gui` selects the image for the host's architecture automatically.

At run time, `-e OC_MIRROR_BINARY=redhat` makes the container download Red Hat's official `oc-mirror` from mirror.openshift.com at start-up (verified against the published `sha256sum.txt` and cached in `data/bin`, so later starts reuse it until Red Hat publishes a new build) and use it instead of the built-in one. `OC_MIRROR_REDHAT_URL` points at a different download location. If the download or verification fails, the built-in `oc-mirror` is used and a warning is logged.

Platform channels can be `stable`, `fast`, `candidate` or `eus` for any release: pick one of the known releases or type a newer one such as 4.23 or 5.0 in the Mirror Configuration page. Operator catalogs are synced for OCP 4.16–4.22 by default; set `CATALOG_OCP_VERSIONS` (for example `"4.20 4.21 4.22 4.23"`) on the container or for `./sync-catalogs.sh` to sync other releases once Red Hat publishes their catalogs.

Every build path runs `sync-catalogs.sh` to pull the latest Red Hat, Certified, and Community operator catalogs (OCP 4.16–4.22) before building the image. Use `--run` to skip fetching and building when you already have a local image.

Open the URL printed by the script in your browser. By default it uses `http://localhost:3000`, but it automatically selects another free host port if `3000` is already in use. The `Web UI:` line in the script output shows the chosen address.

Manage with: `./local-build.sh --stop`, `--restart`, `--status`, `--logs`.

## Features

### Dashboard

Environment overview (oc-mirror version, environment status, pull secret status), operation statistics, recent operations, and quick action buttons. Shows a warning banner when no pull secret is detected.

![Dashboard](docs/screenshots/dashboard.png)

**Dark theme** - Toggle between Light, Dark, and System (auto) themes from the masthead.

![Dashboard Dark Theme](docs/screenshots/dashboard-dark.png)

### Mirror Configuration

Visual configuration builder with tabs for Platform Channels, Operators, Additional Images, YAML Preview, and file upload.

- **Adding operators** - Select from pre-fetched catalogs (OCP 4.16–4.22) with Red Hat, Certified, and Community operator indexes. Automatic dependency detection with one-click add.
- **YAML preview and editing** - Preview the generated `ImageSetConfiguration` YAML, copy to clipboard, or edit directly. Set an optional archive size limit (in GiB).
- **Upload existing YAML** - Import an existing `ImageSetConfiguration` YAML file, review and edit it, then save or load it into the form editor.

![Mirror Configuration](docs/screenshots/config-add-operator.png)

### Mirror Operations

Execute mirror operations with real-time monitoring. Select a saved configuration file, optionally specify a destination subdirectory, and start the operation. View operation history with logs, location info, and delete actions.

The Operation Logs console streams oc-mirror output as it runs. Drag the handle under it to make it taller or shorter (or focus the handle and use the arrow keys; double-click resets it). The height is remembered in the browser. **Expand** fills the window with the logs, and **Collapse** or Escape returns them to the page.

![Mirror Operations](docs/screenshots/mirror-operations.png)

### History

Filter and review all past operations. Export to CSV.

![History](docs/screenshots/history.png)

### Settings

Configure environment preferences across four tabs:

| Tab | Purpose |
|-----|---------|
| **Pull Secret** | View, upload, edit, or remove your pull secret |
| **Registry** | Auto-detected registries from your pull secret with authentication verification |
| **Cache** | View cache location and size, clean up cache data |
| **Sync Catalogs** | Fetch the latest operator catalog metadata from registry.redhat.io for all supported OCP versions |

![Settings](docs/screenshots/settings.png)

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `IMAGE_NAME` | Override the container image name | `mirror-gui:latest` |
| `WEB_PORT` | Override the host port | `3000` |
| `CACHE_DIR` | Override the oc-mirror cache directory (absolute host path) | `./data/cache` |

## Compatibility

| | |
|---|---|
| **oc-mirror** | v2 |
| **OpenShift** | 4.16 – 4.22 |
| **Container runtime** | Podman 5.0+ |
| **Architecture** | AMD64 (x86_64), ARM64 (aarch64); ppc64le and s390x can be built with `release.sh --platforms` |

## Troubleshooting

See [TROUBLESHOOTING.md](TROUBLESHOOTING.md) for common issues and solutions.

## API

Full RESTful API documentation is available in [API.md](API.md).

## Testing

Test documentation is available in [TESTS.md](TESTS.md).

To run tests locally:

```bash
go test ./...         # backend unit and API tests
npm test              # script tests (Vitest)
npm run test:e2e      # end-to-end tests (Playwright, headless Chromium)
go vet ./...          # backend static checks
npm run lint          # frontend ESLint
```

## Development

### Architecture

Mirror-GUI has two main layers:

| Layer | Technology | Purpose |
|-------|-----------|---------|
| **Frontend** | React 18, TypeScript, PatternFly 6, Vite | Single-page application with visual config builder |
| **Backend** | Go (`net/http`) | REST API server that wraps oc-mirror v2 CLI and serves the frontend |

The backend spawns `oc-mirror` as a child process for mirror operations and streams logs via SSE. Operator catalog metadata is pre-fetched at build time (`sync-catalogs.sh`) and bundled into the container image for offline browsing.

For a detailed architecture overview, see [ARCHITECTURE.md](ARCHITECTURE.md). Additional developer reference docs (catalog pipeline, mirror operations lifecycle, registry auth flow) are available in [docs/dev/](docs/dev/).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for details on reporting bugs, submitting pull requests, and running tests.

## License

Apache License 2.0 - see [LICENSE](LICENSE) for details.

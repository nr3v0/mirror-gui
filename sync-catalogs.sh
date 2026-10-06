#!/bin/bash

# Syncs operator catalog metadata from registry.redhat.io for all supported OCP versions.
# The implementation is `mirror-gui sync-catalogs` (internal/catalogmeta); this wrapper
# keeps the historical entry point used by local-build.sh.
# Requires: Go (to build the tool) or a mirror-gui binary via MIRROR_GUI_BIN.
#
# Auth resolution (in priority order):
#   1. PULL_SECRET_PATH env var pointing to an existing file
#   2. REGISTRY_AUTH_FILE env var pointing to an existing file
#   3. Default container credentials (e.g. ~/.docker/config.json, podman auth.json)
#
# Environment variables:
#   CATALOG_DATA_DIR   - output directory (default: ./catalog-data)
#   PULL_SECRET_PATH   - path to pull secret JSON file (default: pull-secret/pull-secret.json)
#   REGISTRY_AUTH_FILE - fallback auth file path (commonly set by Prow CI)
#   MAX_PARALLEL_JOBS  - max concurrent catalog extractions (default: 3)
#   MIRROR_GUI_BIN     - prebuilt mirror-gui binary to use instead of building one

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [ -n "${MIRROR_GUI_BIN:-}" ]; then
    exec "$MIRROR_GUI_BIN" sync-catalogs "$@"
fi

if ! command -v go >/dev/null 2>&1; then
    echo "ERROR: Go is required to build the catalog sync tool (or set MIRROR_GUI_BIN)" >&2
    exit 1
fi

build_dir=$(mktemp -d)
trap 'rm -rf "$build_dir"' EXIT
(cd "$SCRIPT_DIR" && go build -o "$build_dir/mirror-gui" ./cmd/mirror-gui)
"$build_dir/mirror-gui" sync-catalogs "$@"

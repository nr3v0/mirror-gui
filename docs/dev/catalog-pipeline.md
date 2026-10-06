# Catalog Data Pipeline

Mirror-GUI uses pre-fetched operator catalog metadata rather than querying registries at runtime. This enables offline browsing and avoids rate-limiting or authentication issues during catalog exploration.

## How it works

1. The catalog sync (`internal/catalogmeta`) runs `oc image extract` to copy the File-Based Catalog (FBC) configs out of the Red Hat operator index images for each supported OCP version (4.16–4.22) and catalog type (Red Hat, Certified, Community), up to three at a time with three attempts each, and records each image's digest with `oc image info`. It runs in three places:
   - in the server, when a sync is started from the UI (`POST /api/catalogs/sync`);
   - on the host, via `./sync-catalogs.sh` (a wrapper around `mirror-gui sync-catalogs`), which `local-build.sh` runs before every image build;
   - in CI, where `Dockerfile.catalog-sync` pulls the index images with `FROM` and runs `mirror-gui catalog-metadata finalize` on the copied configs.

2. The FBC JSON/YAML documents (`olm.package`, `olm.channel`, `olm.bundle`) are parsed to produce structured JSON files per catalog: `operators.json` (operator names, channels, versions, default channels), `dependencies.json` (inter-operator dependency mappings), and `catalog-info.json` (catalog metadata with digest and sync timestamp).

3. A top-level `catalog-index.json` registers all processed catalogs with their URLs, types, versions, and digests.

4. The server loads this data lazily on first API request and caches it in memory for the lifetime of the process. A runtime sync (triggered from the UI) writes fresh data to a separate runtime directory that takes precedence over the built-in data.

## Catalog sync diffing

When a catalog sync completes, the server computes a diff between the previous and new catalog data. This diff identifies:
- New operators added to a catalog
- Operators removed from a catalog
- Operators with new versions available

The diff is exposed via the sync status API and displayed in the UI, so users can see what changed without comparing raw JSON.

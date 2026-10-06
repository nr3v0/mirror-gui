# Troubleshooting

## Server logs

View container logs to diagnose issues:

```bash
./mirror-gui.sh --logs    # pre-built image
./local-build.sh --logs   # local build
```

Use `mirror-gui.sh` when running a pre-built image from the registry; use `local-build.sh` when building from source locally. Both scripts accept the same `--logs`, `--status`, `--stop`, and `--restart` flags.

The server logs all incoming HTTP requests with timestamps and writes oc-mirror operation output to individual log files under the data directory.

## Container startup issues

If the container fails to start, check that the required port is available:

```bash
# Check if port 3000 is in use
ss -tlnp | grep 3000
```

Both scripts automatically select the next available port if `3000` is occupied. Look for the `Web UI:` line in the script output.

If the container starts but the UI is unreachable, verify the container is running:

```bash
./mirror-gui.sh --status    # pre-built image
./local-build.sh --status   # local build
```

## Pull secret issues

Mirror-GUI requires a valid pull secret to authenticate with container registries. If the dashboard shows "No pull secret detected":

1. Upload a pull secret via **Settings > Pull Secret** in the UI
2. Or place it at `pull-secret/pull-secret.json` before building the container

The pull secret must be valid JSON in the standard Docker/Podman auth format with an `auths` key.

To verify registry authentication, use the **Settings > Registry** tab — it shows auto-detected registries from your pull secret and can test authentication against each one.

## Catalog data issues

Mirror-GUI bundles pre-fetched operator catalog metadata at build time. If operators or channels appear missing or outdated:

1. Use **Settings > Sync Catalogs** to fetch the latest catalog metadata from registry.redhat.io
2. This requires a valid pull secret with access to `registry.redhat.io`
3. The sync processes three catalog types (Red Hat, Certified, Community) across OCP 4.16–4.22

If the sync fails, check that your pull secret has valid credentials for `registry.redhat.io`. See the sync logs in the Settings tab for specific errors.

To reset to built-in catalog data, use the "Clear Synced Data" option in the Sync Catalogs tab.

## Mirror operation failures

When a mirror operation fails:

1. Check the operation logs — click on the failed operation in **Mirror Operations** or **History** to view logs
2. Common issues:
   - **Authentication failures**: Verify pull secret credentials for the source registries
   - **Disk space**: Check available disk space in the mirror destination and cache directories
   - **Invalid configuration**: Verify the ImageSetConfiguration YAML is valid — the config builder validates structure on save
   - **Network issues**: Ensure the container can reach the source registries

## GPG signature errors

**Invalid GPG signature for operator index images** — This is a known issue with some Red Hat operator index images. See [Red Hat KB article](https://access.redhat.com/solutions/6542281) for resolution.

## Cache issues

The oc-mirror cache can grow large over time. To clean it up:

1. Use **Settings > Cache** to view cache size and clean up
2. Or restart the container — the cache is ephemeral unless a persistent volume is mounted via `CACHE_DIR`

## Development issues

### Tests failing locally

```bash
# Backend tests, verbose
go test -v ./...

# Run a specific backend test
go test ./internal/server -run TestConfigSaveAndValidation

# Frontend/script tests
npm ci
npm test -- --reporter=verbose
```

### E2E tests failing

E2E tests require Playwright browsers to be installed:

```bash
npx playwright install chromium
npm run test:e2e
```

### Port conflicts in development

The backend runs on port 3001 and the Vite dev server on 3000 by default. If 3001 conflicts:

```bash
PORT=3002 npm run dev:server
MIRROR_GUI_API_URL=http://localhost:3002 npm run dev
```

# Multi-arch builds (podman build --platform linux/amd64,linux/arm64) cross-compile:
# every stage that runs commands uses the build host's platform, Go builds for
# TARGETARCH, and only the final scratch image differs per architecture.

# Frontend: the only stage that uses Node.js.
FROM --platform=$BUILDPLATFORM registry.access.redhat.com/ubi9/nodejs-22-minimal AS frontend
USER root
WORKDIR /src
COPY package.json package-lock.json .npmrc ./
RUN npm ci --no-fund --no-audit --fetch-timeout=300000 --fetch-retries=5 \
        --fetch-retry-mintimeout=20000 --fetch-retry-maxtimeout=120000
COPY index.html tsconfig.json vite.config.ts vite-env.d.ts ./
COPY public ./public
COPY src ./src
RUN VITE_SOURCEMAP=false npx vite build

# The app version, captured here because go-toolset defines its own VERSION
# environment variable, which would shadow the build argument in that stage.
FROM --platform=$BUILDPLATFORM registry.access.redhat.com/ubi9/ubi-micro AS version
ARG VERSION=dev
RUN echo "$VERSION" > /version

# Backend binary, oc-mirror and built-in catalog data.
FROM --platform=$BUILDPLATFORM registry.access.redhat.com/ubi9/go-toolset:1.26 AS builder
USER root
ARG TARGETARCH
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY --from=version /version /tmp/version
RUN CGO_ENABLED=0 GOARCH="${TARGETARCH:-$(go env GOARCH)}" go build -trimpath \
        -ldflags="-s -w -X github.com/nr3v0/mirror-gui/internal/server.Version=$(cat /tmp/version)" \
        -o /out/app/mirror-gui ./cmd/mirror-gui && \
    mkdir -p /out/app/data

# oc-mirror, from one of two sources (build argument OC_MIRROR_FROM):
#   source (default): compile github.com/openshift/oc-mirror at OC_MIRROR_REF with
#     this stage's Go toolchain, after upgrading the modules in OC_MIRROR_GO_UPGRADES
#     (set it to "" to build upstream unmodified). Only the v2 CLI is built; the
#     embedded v1 binary (used by --v1 only) is left out. Needs network access.
#   redhat: Red Hat's published binary, prefetched by Konflux (cachi2) or downloaded.
ARG OC_MIRROR_FROM=source
ARG OC_MIRROR_REPO=https://github.com/openshift/oc-mirror.git
# The commit of Red Hat's oc-mirror 2.0.0-202610201601 build.
ARG OC_MIRROR_REF=c4d52faef556ec6778b200005df679bf981aec4e
ARG OC_MIRROR_GO_UPGRADES="\
    golang.org/x/net@latest \
    golang.org/x/crypto@latest \
    github.com/distribution/distribution/v3@latest \
    github.com/google/cel-go@v0.31.0 \
    go.opentelemetry.io/otel/sdk@latest \
    go.opentelemetry.io/otel/sdk/log@latest \
    go.opentelemetry.io/otel/exporters/otlp/otlptrace@latest \
    go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc@latest \
    go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp@latest \
    go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc@latest"
RUN set -eux; \
    arch="${TARGETARCH:-$(go env GOARCH)}"; \
    case "$OC_MIRROR_FROM" in \
    source) \
      src=/tmp/oc-mirror-src; \
      git init -q "$src"; \
      git -C "$src" fetch -q --depth 1 "$OC_MIRROR_REPO" "$OC_MIRROR_REF"; \
      git -C "$src" checkout -q FETCH_HEAD; \
      commit=$(git -C "$src" rev-parse --short=8 HEAD); \
      cd "$src"; \
      if [ -n "$(echo $OC_MIRROR_GO_UPGRADES)" ]; then \
        go get $OC_MIRROR_GO_UPGRADES; \
        go mod tidy; \
      fi; \
      pkg=github.com/openshift/oc-mirror/v2/internal/pkg/version; \
      CGO_ENABLED=0 GOARCH="$arch" go build -mod=readonly -trimpath \
        -tags "json1 exclude_graphdriver_devicemapper exclude_graphdriver_btrfs containers_image_openpgp" \
        -ldflags "-s -w -X $pkg.versionFromGit=v2.0.0-source-$commit -X $pkg.commitFromGit=$commit \
          -X $pkg.majorFromGit=2 -X $pkg.minorFromGit=0 -X $pkg.gitTreeState=patched \
          -X $pkg.buildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
        -o /out/oc-mirror ./cmd/oc-mirror; \
      cd /; rm -rf "$src"; \
      ;; \
    redhat) \
      file="oc-mirror-rhel9-linux-${arch}.tar.gz"; \
      tarball="/cachi2/output/deps/generic/$file"; \
      if [ ! -f "$tarball" ]; then \
        tarball="/tmp/$file"; \
        curl -fsSL --retry 5 -o "$tarball" "https://mirror.openshift.com/pub/cgw/oc-mirror/latest/$file"; \
      fi; \
      tar --no-same-owner -xzf "$tarball" -C /out oc-mirror; \
      if [ "$arch" = "$(go env GOHOSTARCH)" ]; then strip /out/oc-mirror; fi; \
      ;; \
    *) echo "OC_MIRROR_FROM must be 'source' or 'redhat', got '$OC_MIRROR_FROM'" >&2; exit 1 ;; \
    esac; \
    if [ "$arch" = "$(go env GOHOSTARCH)" ]; then /out/oc-mirror version --output=json; \
    else echo "Built oc-mirror for $arch (not runnable on $(go env GOHOSTARCH); skipping version check)"; fi

# Built-in catalog metadata: catalog-data-synced (injected by CI) or catalog-data, without raw configs.
COPY . /ctx
RUN set -eux; \
    src=/ctx/catalog-data-synced; \
    [ -f "$src/catalog-index.json" ] || src=/ctx/catalog-data; \
    dest=/out/app/catalog-data; \
    mkdir -p "$dest"; \
    if [ -f "$src/catalog-index.json" ]; then \
      cp "$src/catalog-index.json" "$dest/"; \
    else \
      echo '{"ocp_versions":[],"catalog_types":[],"catalogs":[]}' > "$dest/catalog-index.json"; \
    fi; \
    cd "$src"; \
    find . -path '*/configs' -prune -o -type f \
      \( -name operators.json -o -name dependencies.json -o -name catalog-info.json \) -print | \
      while read -r f; do install -D -m 0644 "$f" "$dest/$f"; done

# Minimal root filesystem for the final image: CA certificates, users, /tmp.
# Everything in the image is a static binary, so no OS packages are needed.
RUN set -eux; \
    root=/out/rootfs; \
    mkdir -p "$root/etc/pki/tls" "$root/etc/pki/ca-trust" "$root/usr/local/bin" "$root/tmp"; \
    chmod 1777 "$root/tmp"; \
    cp -a /etc/pki/ca-trust/extracted "$root/etc/pki/ca-trust/"; \
    cp -a /etc/pki/tls/certs "$root/etc/pki/tls/"; \
    mkdir -p "$root/etc/ssl"; ln -s /etc/pki/tls/certs "$root/etc/ssl/certs"; \
    printf 'root:x:0:0:root:/root:/sbin/nologin\ndefault:x:1001:0:mirror-gui:/app:/sbin/nologin\n' > "$root/etc/passwd"; \
    printf 'root:x:0:\n' > "$root/etc/group"; \
    mv /out/oc-mirror "$root/usr/local/bin/oc-mirror"

FROM scratch

ARG OC_MIRROR_FROM=source
ARG BUILD_DATE=""
ARG VCS_REF=""
ARG VERSION=dev

COPY --from=builder /out/rootfs /
COPY --from=builder --chown=1001:0 /out/app /app
COPY --from=frontend --chown=1001:0 /src/dist /app/dist

LABEL org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.title="Mirror-GUI Application" \
      org.opencontainers.image.description="Web application for OpenShift Container Platform mirroring operations" \
      org.opencontainers.image.source="https://github.com/nr3v0/mirror-gui" \
      io.github.nr3v0.mirror-gui.oc-mirror="${OC_MIRROR_FROM}"

WORKDIR /app
# The server starts as root, fixes ownership of mounted data, then runs as uid 1001.
# OC_MIRROR_BUILT_FROM records where the built-in oc-mirror came from. Choose
# "Official Red Hat Download" on the Dashboard, or set OC_MIRROR_BINARY=redhat,
# to download and use Red Hat's official oc-mirror instead (verified, cached in
# /app/data/bin).
ENV HOME=/app PATH=/usr/local/bin MIRROR_GUI_RUN_AS=1001:0 OC_MIRROR_BUILT_FROM=${OC_MIRROR_FROM}
EXPOSE 3001
ENTRYPOINT ["/app/mirror-gui"]

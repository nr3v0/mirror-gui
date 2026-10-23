# Frontend: the only stage that uses Node.js.
FROM registry.access.redhat.com/ubi9/nodejs-22-minimal AS frontend
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
FROM registry.access.redhat.com/ubi9/ubi-micro AS version
ARG VERSION=dev
RUN echo "$VERSION" > /version

# Backend binary, oc-mirror and built-in catalog data.
FROM registry.access.redhat.com/ubi9/go-toolset:1.26 AS builder
USER root
ARG TARGETARCH
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY --from=version /version /tmp/version
RUN CGO_ENABLED=0 go build -trimpath \
        -ldflags="-s -w -X github.com/nr3v0/mirror-gui/internal/server.Version=$(cat /tmp/version)" \
        -o /out/app/mirror-gui ./cmd/mirror-gui && \
    mkdir -p /out/app/data

# oc-mirror is prefetched by Konflux (cachi2) or downloaded; its debug symbols are stripped.
RUN set -eux; \
    arch=amd64; [ "$TARGETARCH" = arm64 ] && arch=arm64; \
    file="oc-mirror-rhel9-linux-${arch}.tar.gz"; \
    src="/cachi2/output/deps/generic/$file"; \
    if [ ! -f "$src" ]; then \
      src="/tmp/$file"; \
      curl -fsSL --retry 5 -o "$src" "https://mirror.openshift.com/pub/cgw/oc-mirror/latest/$file"; \
    fi; \
    tar --no-same-owner -xzf "$src" -C /out oc-mirror; \
    strip /out/oc-mirror; \
    /out/oc-mirror version --output=json

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
      org.opencontainers.image.source="https://github.com/nr3v0/mirror-gui"

WORKDIR /app
# The server starts as root, fixes ownership of mounted data, then runs as uid 1001.
ENV HOME=/app PATH=/usr/local/bin MIRROR_GUI_RUN_AS=1001:0
EXPOSE 3001
ENTRYPOINT ["/app/mirror-gui"]

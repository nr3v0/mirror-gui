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

# Backend binary, oc-mirror and built-in catalog data.
FROM registry.access.redhat.com/ubi9/go-toolset:1.26 AS builder
USER root
ARG TARGETARCH
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app/mirror-gui ./cmd/mirror-gui && \
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

FROM registry.access.redhat.com/ubi9/ubi-micro

ARG BUILD_DATE=""
ARG VCS_REF=""
ARG VERSION=1.0

COPY --from=builder /etc/pki/ca-trust/extracted /etc/pki/ca-trust/extracted
COPY --from=builder /etc/pki/tls/certs /etc/pki/tls/certs
COPY --from=builder /out/oc-mirror /usr/local/bin/oc-mirror
COPY --from=builder --chown=1001:0 /out/app /app
COPY --from=frontend --chown=1001:0 /src/dist /app/dist
RUN echo 'default:x:1001:0:mirror-gui:/app:/sbin/nologin' >> /etc/passwd

LABEL org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.title="Mirror-GUI Application" \
      org.opencontainers.image.description="Web application for OpenShift Container Platform mirroring operations" \
      org.opencontainers.image.source="https://github.com/openshift/mirror-gui"

WORKDIR /app
# The server starts as root, fixes ownership of mounted data, then runs as uid 1001.
ENV HOME=/app MIRROR_GUI_RUN_AS=1001:0
EXPOSE 3001
ENTRYPOINT ["/app/mirror-gui"]

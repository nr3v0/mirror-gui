# Node.js is only used here to build the React frontend; the runtime image has no Node.js or npm.
FROM registry.access.redhat.com/ubi9/nodejs-22-minimal AS builder

USER root

WORKDIR /app

COPY package*.json ./
RUN npm config set fetch-timeout 300000 && \
    npm config set fetch-retries 5 && \
    npm config set fetch-retry-mintimeout 20000 && \
    npm config set fetch-retry-maxtimeout 120000 && \
    if [ -f package-lock.json ]; then \
      npm ci --no-fund --no-audit; \
    else \
      npm install --no-fund --no-audit; \
    fi

COPY . .
RUN mkdir -p /app/catalog-data-minimal && \
    if [ -d /app/catalog-data-synced ] && [ -f /app/catalog-data-synced/catalog-index.json ]; then \
      CATALOG_SRC=/app/catalog-data-synced; \
    else \
      CATALOG_SRC=/app/catalog-data; \
    fi && \
    (cp "$CATALOG_SRC/catalog-index.json" /app/catalog-data-minimal/ 2>/dev/null || \
     echo '{"ocp_versions":[],"catalog_types":[],"catalogs":[]}' > /app/catalog-data-minimal/catalog-index.json) && \
    find "$CATALOG_SRC" -type f \( -name "operators.json" -o -name "dependencies.json" -o -name "catalog-info.json" \) ! -path "*/configs/*" 2>/dev/null | while read file; do \
      rel_path=$(echo "$file" | sed "s|$CATALOG_SRC/||"); \
      mkdir -p "/app/catalog-data-minimal/$(dirname "$rel_path")"; \
      cp "$file" "/app/catalog-data-minimal/$rel_path"; \
    done
RUN npx vite build

# Build the Go backend as a static binary.
FROM registry.access.redhat.com/ubi9/go-toolset:1.26 AS gobuilder

USER root

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mirror-gui ./cmd/mirror-gui

# Fetch oc-mirror and oc; wget/tar stay in this stage (not copied to production).
FROM registry.access.redhat.com/ubi9/ubi-minimal AS downloader

USER root

ARG TARGETARCH

RUN microdnf install -y --nodocs wget tar gzip gpgme && \
    microdnf clean all

ENV OCMIRROR_URL_AMD64="https://mirror.openshift.com/pub/cgw/oc-mirror/latest/oc-mirror-rhel9-linux-amd64.tar.gz"
ENV OCMIRROR_URL_ARM64="https://mirror.openshift.com/pub/cgw/oc-mirror/latest/oc-mirror-rhel9-linux-arm64.tar.gz"

RUN set -eux; \
    if [ "$TARGETARCH" = "arm64" ]; then \
      OCMIRROR_FILE="oc-mirror-rhel9-linux-arm64.tar.gz"; \
    else \
      OCMIRROR_FILE="oc-mirror-rhel9-linux-amd64.tar.gz"; \
    fi; \
    if [ -f "/cachi2/output/deps/generic/$OCMIRROR_FILE" ]; then \
      echo "Using prefetched oc-mirror binary"; \
      tar --no-same-owner -xzf "/cachi2/output/deps/generic/$OCMIRROR_FILE" -C /usr/local/bin/; \
    else \
      if [ "$TARGETARCH" = "arm64" ]; then \
        OCMIRROR_URL=$OCMIRROR_URL_ARM64; \
      else \
        OCMIRROR_URL=$OCMIRROR_URL_AMD64; \
      fi; \
      wget -O /tmp/oc-mirror.tar.gz "$OCMIRROR_URL"; \
      tar --no-same-owner -xzf /tmp/oc-mirror.tar.gz -C /usr/local/bin/; \
      rm /tmp/oc-mirror.tar.gz; \
    fi; \
    chmod +x /usr/local/bin/oc-mirror; \
    command -v oc-mirror; \
    oc-mirror version

# oc CLI is used at runtime by the in-app catalog sync (oc image extract / oc image info).
RUN set -eux; \
    if [ "$TARGETARCH" = "arm64" ]; then \
      OC_FILE="openshift-client-linux-arm64.tar.gz"; \
      OC_URL="https://mirror.openshift.com/pub/openshift-v4/aarch64/clients/ocp/stable/openshift-client-linux.tar.gz"; \
    else \
      OC_FILE="openshift-client-linux-amd64.tar.gz"; \
      OC_URL="https://mirror.openshift.com/pub/openshift-v4/clients/ocp/stable/openshift-client-linux.tar.gz"; \
    fi; \
    if [ -f "/cachi2/output/deps/generic/$OC_FILE" ]; then \
      echo "Using prefetched oc CLI binary"; \
      tar --no-same-owner -xzf "/cachi2/output/deps/generic/$OC_FILE" -C /usr/local/bin oc; \
    else \
      wget -qO /tmp/oc.tar.gz "$OC_URL"; \
      tar --no-same-owner -xzf /tmp/oc.tar.gz -C /usr/local/bin oc; \
      rm /tmp/oc.tar.gz; \
    fi; \
    oc version --client

FROM registry.access.redhat.com/ubi9/ubi-minimal AS production

USER root

ARG BUILD_DATE=""
ARG VCS_REF=""
ARG VERSION=1.0

COPY --from=downloader /usr/local/bin/oc-mirror /usr/local/bin/oc-mirror
COPY --from=downloader /usr/local/bin/oc /usr/local/bin/oc

# gpgme: oc-mirror runtime dependency.
# util-linux (runuser/su) and shadow-utils (useradd): entrypoint.sh.
RUN microdnf install -y --nodocs \
        bash gpgme \
        util-linux shadow-utils && \
    microdnf clean all && \
    # ubi-minimal has no named app user; create UBI convention uid 1001 (default).
    useradd --uid 1001 --gid 0 --home-dir /app --no-create-home \
        --shell /sbin/nologin default

RUN set -eux; \
    oc-mirror version; \
    oc version --client

WORKDIR /app

COPY --from=gobuilder /out/mirror-gui ./mirror-gui
COPY --from=builder /app/dist ./dist

# Copy only generated catalog metadata required at runtime.
COPY --from=builder /app/catalog-data-minimal ./catalog-data

RUN mkdir -p /app/data && chown -R 1001:0 /app

LABEL org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.title="Mirror-GUI Application" \
      org.opencontainers.image.description="Web application for OpenShift Container Platform mirroring operations" \
      org.opencontainers.image.source="https://github.com/openshift/mirror-gui"

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 3001

ENTRYPOINT ["/entrypoint.sh"]
CMD ["/app/mirror-gui"]

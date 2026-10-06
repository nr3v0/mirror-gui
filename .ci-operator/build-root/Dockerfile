FROM registry.access.redhat.com/ubi9/nodejs-22-minimal

USER root

RUN microdnf install -y --nodocs \
    git \
    nspr nss nss-util \
    alsa-lib atk at-spi2-atk cups-libs libdrm \
    libXcomposite libXdamage libXrandr mesa-libgbm pango \
    libxkbcommon libXfixes libXcursor libXext libXi libXtst \
    libwayland-client \
    && microdnf clean all

# Go toolchain for the backend (go build / go test).
COPY --from=registry.access.redhat.com/ubi9/go-toolset:1.26 /usr/lib/golang /usr/lib/golang
RUN ln -s /usr/lib/golang/bin/go /usr/local/bin/go && \
    ln -s /usr/lib/golang/bin/gofmt /usr/local/bin/gofmt

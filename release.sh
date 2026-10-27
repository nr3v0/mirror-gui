#!/bin/bash

# Mirror-GUI — build a multi-arch release image (Podman) and optionally push it.
#
# Usage: ./release.sh [--push] [--no-latest] [--platforms LIST] [--image NAME] VERSION
#
#   VERSION            release version without the leading v, e.g. 5.2.0
#   --push             push the manifest list to the registry after building
#   --no-latest        do not tag the release as latest
#   --platforms LIST   comma-separated platforms (env PLATFORMS;
#                      default linux/amd64,linux/arm64; linux/ppc64le and linux/s390x also work)
#   --image NAME       image repository (env IMAGE; default quay.io/nr3v0/mirror-gui)
#
# The image is cross-compiled (see the Dockerfile), so no QEMU is needed.
# Tags: vVERSION, the short git commit and, unless --no-latest, latest.
# OC_MIRROR_FROM=source|redhat is passed through as a build argument.

set -euo pipefail

IMAGE="${IMAGE:-quay.io/nr3v0/mirror-gui}"
PLATFORMS="${PLATFORMS:-linux/amd64,linux/arm64}"
OC_MIRROR_FROM="${OC_MIRROR_FROM:-source}"
PUSH=false
LATEST=true
VERSION=""

usage() {
    sed -n '3,16p' "$0" | sed 's/^# \{0,1\}//'
}

while [ $# -gt 0 ]; do
    case "$1" in
        --push) PUSH=true ;;
        --no-latest) LATEST=false ;;
        --platforms) PLATFORMS="${2:?--platforms needs a value}"; shift ;;
        --image) IMAGE="${2:?--image needs a value}"; shift ;;
        -h|--help) usage; exit 0 ;;
        -*) echo "Unknown option: $1" >&2; usage >&2; exit 2 ;;
        *) VERSION="${1#v}" ;;
    esac
    shift
done

if [ -z "$VERSION" ]; then
    usage >&2
    exit 2
fi

cd "$(dirname "$0")"

if [ -n "$(git status --porcelain --untracked-files=no)" ]; then
    echo "WARNING: the working tree has uncommitted changes; they will be in the image." >&2
fi

COMMIT="$(git rev-parse --short HEAD)"
TAGS=("v$VERSION" "$COMMIT")
if [ "$LATEST" = true ]; then
    TAGS+=("latest")
fi

MANIFEST="$IMAGE:v$VERSION"
# podman build --manifest adds to an existing list, so start from a fresh one.
podman manifest rm "$MANIFEST" >/dev/null 2>&1 || true

echo "Building $MANIFEST for $PLATFORMS (oc-mirror: $OC_MIRROR_FROM)"
podman build \
    --pull=always \
    --platform "$PLATFORMS" \
    --manifest "$MANIFEST" \
    --build-arg VERSION="$VERSION" \
    --build-arg VCS_REF="$(git rev-parse HEAD)" \
    --build-arg BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --build-arg OC_MIRROR_FROM="$OC_MIRROR_FROM" \
    .

for tag in "${TAGS[@]:1}"; do
    podman manifest rm "$IMAGE:$tag" >/dev/null 2>&1 || true
    podman tag "$MANIFEST" "$IMAGE:$tag"
done

podman manifest inspect "$MANIFEST" |
    python3 -c 'import json,sys; [print("  ", m["platform"]["os"] + "/" + m["platform"]["architecture"], m["digest"]) for m in json.load(sys.stdin)["manifests"]]'

if [ "$PUSH" = true ]; then
    for tag in "${TAGS[@]}"; do
        echo "Pushing $IMAGE:$tag"
        podman manifest push --all "$IMAGE:$tag" "docker://$IMAGE:$tag"
    done
else
    echo "Built ${TAGS[*]/#/$IMAGE:}. Re-run with --push to publish."
fi

#!/bin/sh
# R11.2 + R11.14: build both required platforms and publish them as one multi-arch
# image, without needing `docker login`.
#
# `docker buildx build --push` needs registry credentials for the target registry;
# this script builds each platform to an OCI layout with `-o type=oci,dir=…` (which
# needs no credentials at all) and then publishes with scripts/push-oci.py, which
# speaks the registry v2 API directly. See IMPLEMENTATION.md §5 for why.
#
# Usage: scripts/build-push-dev.sh [REGISTRY] [REPO] [TAG]
#   scripts/build-push-dev.sh                                  # reference registry, :dev
#   scripts/build-push-dev.sh ghcr.io frankellis/pgsql-insights v0.1.0
set -e

REGISTRY=${1:-${REGISTRY:-10.10.111.134:5000}}
REPO=${2:-${REPO:-frankellis/pgsql-insights}}
TAG=${3:-${TAG:-dev}}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
VERSION=${VERSION:-$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)}
COMMIT=${COMMIT:-$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)}
BUILDER=${BUILDER:-}
BUILDKIT=""
[ -n "$BUILDER" ] && BUILDKIT="--builder $BUILDER"

for arch in amd64 arm64; do
  dir="$ROOT/bin/oci-$arch"
  echo "==> building linux/$arch ($(VERSION=$VERSION COMMIT=$COMMIT))"
  rm -rf "$dir"
  mkdir -p "$dir"
  docker buildx build $BUILDKIT --platform "linux/$arch" \
    --build-arg VERSION="$VERSION" --build-arg COMMIT="$COMMIT" \
    -t "$REGISTRY/$REPO:$TAG" \
    --output type=oci,dest="$dir/image.oci.tar" "$ROOT"
  tar -C "$dir" -xf "$dir/image.oci.tar"
  rm -f "$dir/image.oci.tar"
done

echo "==> publishing $REGISTRY/$REPO:$TAG"
python3 "$ROOT/scripts/push-oci.py" --registry "$REGISTRY" --repo "$REPO" --tag "$TAG" \
  "$ROOT/bin/oci-amd64" "$ROOT/bin/oci-arm64"

echo "==> R11.4 size report"
python3 "$ROOT/scripts/image-size.py" "$REGISTRY/$REPO:$TAG"

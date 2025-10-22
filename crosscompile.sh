#!/usr/bin/env bash

set -eux

VARIANT="${1:-}"
if [ -z "$VARIANT" ]; then
    echo "Usage: $0 (x86_64|armv6|armv6_rpi|arm64)"
    exit 1
fi

# Validate and map variant to compilation envs
if [ "$VARIANT" = "x86_64" ]; then
  TARGET="x86-64-linux-gnu"
  TRIPLET="x64-linux"
  GOARCH="amd64"
  GOAMD64="v1"
  GOARM=""
  GOCC="gcc"
elif [ "$VARIANT" = "armv6" ]; then
  TARGET="arm-linux-gnueabihf"
  TRIPLET="arm-linux"
  GOARCH="arm"
  GOAMD64=""
  GOARM="6"
  GOCC="arm-linux-gnueabihf-gcc"
elif [ "$VARIANT" = "armv6_rpi" ]; then
  TARGET="arm-rpi-linux-gnueabihf"
  TRIPLET="arm-linux-rpi"
  GOARCH="arm"
  GOAMD64=""
  GOARM="6"
  GOCC="arm-rpi-linux-gnueabihf-gcc"
elif [ "$VARIANT" = "arm64" ]; then
  TARGET="aarch64-linux-gnu"
  TRIPLET="arm64-linux"
  GOARCH="arm64"
  GOAMD64=""
  GOARM=""
  GOCC="aarch64-linux-gnu-gcc"
else
  echo "Unsupported variant: $VARIANT"
  exit 1
fi

DOCKER_IMAGE_NAME="go-librespot-build-${VARIANT}"

# Build the image for cross-compilation
docker build \
  --build-arg "TARGET=$TARGET" \
  --build-arg "TRIPLET=$TRIPLET" \
  --build-arg "GOARCH=$GOARCH" \
  --build-arg "GOAMD64=$GOAMD64" \
  --build-arg "GOARM=$GOARM" \
  --build-arg "GOCC=$GOCC" \
  -f Dockerfile.build \
  -t "$DOCKER_IMAGE_NAME" \
  .

# Build go-librespot using the cross-compilation image
docker run --rm \
  -u "$(id -u):$(id -g)" \
  -v "$PWD":/src \
  "$DOCKER_IMAGE_NAME"

# Print info about the built binary
mv "./go-librespot" "./go-librespot-$VARIANT"
file "./go-librespot-$VARIANT"

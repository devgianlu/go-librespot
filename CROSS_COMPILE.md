# Cross Compiling

Cross compilation is currently described for Linux only. All the commands below assume that the host is `x86_64`.

## Create Docker images for (cross-)compiling

### Target Linux x86_64

```bash
docker build \
  --build-arg TARGET=x86-64-linux-gnu \
  --build-arg GOARCH=amd64 \
  --build-arg CC=gcc \
  -f Dockerfile.build \
  -t go-librespot-build-x86_64 .
```

### Target Linux on Raspberry Pi 1 / Zero

```bash
docker build \
  --build-arg TARGET=arm-rpi-linux-gnueabihf \
  --build-arg GOARCH=arm \
  --build-arg CC=arm-rpi-linux-gnueabihf-gcc \
  -f Dockerfile.build \
  -t go-librespot-build-armv6_rpi .
```

### Target Linux ARM32 (Raspberry Pi 2 and above)

```bash
docker build \
  --build-arg TARGET=arm-linux-gnueabihf \
  --build-arg GOARCH=arm \
  --build-arg CC=arm-linux-gnueabihf-gcc \
  -f Dockerfile.build \
  -t go-librespot-build-armv6 .
```

### Target Linux ARM64

```bash
docker build \
  --build-arg TARGET=aarch64-linux-gnu \
  --build-arg GOARCH=arm64 \
  --build-arg CC=aarch64-linux-gnu-gcc \
  -f Dockerfile.build \
  -t go-librespot-build-arm64 .
```

## Use the images built above to (cross-)compile

`cd` into the root of the `go-librespot` source code and run on of the following statements.

### Target Linux x86_64

```bash
docker run --rm -v $PWD:/src -e GOOUTSUFFIX=-x86_64 go-librespot-build-x86_64
```

### Target Linux on Raspberry Pi 1 / Zero

```bash
docker run --rm -v $PWD:/src -e GOOUTSUFFIX=-armv6_rpi go-librespot-build-armv6_rpi
```

### Target Linux ARM32 (Raspberry Pi 2 and above)

```bash
docker run --rm -v $PWD:/src -e GOOUTSUFFIX=-armv6 go-librespot-build-armv6
```

### Target Linux ARM64

```bash
docker run --rm -v $PWD:/src g-e GOOUTSUFFIX=-arm64 go-librespot-build-arm64
```
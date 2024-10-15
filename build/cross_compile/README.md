# (Cross-)Compile and Build `go-librespot`


## Create Docker image(s) for (cross-)compiling and building `go-librespot`

### Target Linux x86_64
`docker build -t go-librespot-build-x86_64 .`

### Target Linux on Raspberry Pi 1 / Zero
`docker build --build-arg TARGET=arm-rpi-linux-gnueabihf --build-arg GOARCH=arm --build-arg CC=arm-rpi-linux-gnueabihf-gcc --build-arg GOOUTSUFFIX=-armv6_rpi -t go-librespot-build-armv6_rpi .`

### Target Linux ARM32 (Raspberry Pi 2 and above)
`docker build --build-arg TARGET=arm-linux-gnueabihf --build-arg GOARCH=arm --build-arg CC=arm-linux-gnueabihf-gcc --build-arg GOOUTSUFFIX=-armv6 -t go-librespot-build-armv6 .`

### Target Linux ARM64
`docker build --build-arg TARGET=aarch64-linux-gnu --build-arg GOARCH=arm64 --build-arg CC=aarch64-linux-gnu-gcc --build-arg GOOUTSUFFIX=-arm64 -t go-librespot-build-arm64 .`


## Use the image(s) built above to (cross-)compile/build `go-librespot`
`cd` into the root of the `go-librespot` source code and run on of the following statements.

### Target Linux x86_64
`docker run -it --rm -v $PWD:/src go-librespot-build-x86_64`

### Target Linux on Raspberry Pi 1 / Zero
`docker run -it --rm -v $PWD:/src go-librespot-build-armv6_rpi`

### Target Linux ARM32 (Raspberry Pi 2 and above)
`docker run -it --rm -v $PWD:/src go-librespot-build-armv6`

### Target Linux ARM64
`docker run -it --rm -v $PWD:/src go-librespot-build-arm64`

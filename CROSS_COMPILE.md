# Cross Compiling

The cross compilation helper script uses Docker to build the project for different architectures with the respective
toolchains.

Currently, cross compilation is available only for Linux x64 hosts. This is enforced by Docker with the `--platform`
flag set to `linux/amd64`. You can still use this helper on other platforms, but you will need to set up
[Multi-platform builds](https://docs.docker.com/build/building/multi-platform/).

## How To

A handy script to build the project for multiple platforms is provided [here](./crosscompile.sh). To use it simply
specify the target variant you want to build for.

```bash
./crosscompile.sh <variant>
```

The currently supported variants are `x86_64`, `armv6`, `armv6_rpi` and `arm64`.

## `armv6` vs `armv6_rpi`

The `armv6` variant is a generic armv6 build. It should work on most armv6 devices that support thumb instructions.

The `armv6_rpi` variant is specifically built for the Raspberry Pi 1 and Zero, which do not support thumb instructions.
The toolchain used for this build can be found [here](https://github.com/devgianlu/rpi-toolchain).

## Windows (native C libraries)

Windows builds need the same decode libraries as Linux (`libogg`, `libvorbis`, `libflac`, `mpg123`) compiled with MinGW so they can be linked through CGO. ALSA is Linux-only and is skipped automatically.

From a checkout that already has vcpkg bootstrapped:

```shell
vcpkg install --triplet x64-mingw-static
```

The overlay triplet lives in [`vcpkg-triplets/x64-mingw-static.cmake`](./vcpkg-triplets/x64-mingw-static.cmake). Point CGO at the installed pkg-config files:

```
PKG_CONFIG_PATH=<repo>/vcpkg_installed/x64-mingw-static/lib/pkgconfig
CGO_ENABLED=1
CC=x86_64-w64-mingw32-gcc
```

The Windows default `audio_backend` is `wasapi`: a first-party shared-mode WASAPI driver (default playback device, software volume). It does not use `go-wca`.

CI compiles `windows/amd64` two ways: natively on `windows-2022` (MSYS2 packages) and by cross-compiling from Ubuntu with this vcpkg triplet. PR artifacts are uploaded as `go-librespot-windows-amd64` and `go-librespot-windows-amd64-vcpkg`. Tagged releases include `go-librespot_windows_amd64.tar.gz`.
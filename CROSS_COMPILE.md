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
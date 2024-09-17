from docker.io/golang:1.22.2-bookworm

# Preparations
RUN apt-get update && apt-get install -y wget build-essential


# Download and extract dependency sources
RUN cd /tmp && \
    wget http://www.alsa-project.org/files/pub/lib/alsa-lib-1.2.10.tar.bz2 && \
    tar -xvf alsa-lib-1.2.10.tar.bz2
RUN cd /tmp && \
    wget https://downloads.xiph.org/releases/ogg/libogg-1.3.5.tar.xz && \
    tar -xvf libogg-1.3.5.tar.xz
RUN cd /tmp && \
    wget https://downloads.xiph.org/releases/vorbis/libvorbis-1.3.7.tar.xz && \
    tar -xvf libvorbis-1.3.7.tar.xz


ARG TARGET=x86-64-linux-gnu CC=gcc

# Install toolchain for anything besides arm-rpi-linux-gnueabihf
RUN if [ ${TARGET} != arm-rpi-linux-gnueabihf ]; then \
        apt-get install -y gcc-${TARGET} ; \
    fi

# Install custom toolchain for arm-rpi-linux-gnueabihf
RUN if [ ${TARGET} = arm-rpi-linux-gnueabihf ]; then \
        cd /tmp && \
        wget https://github.com/devgianlu/rpi-toolchain/releases/download/v1/arm-rpi-linux-gnueabihf.tar.gz && \
        tar -C /usr --strip-components=1 -xzf arm-rpi-linux-gnueabihf.tar.gz ; \
    fi


# Compile dependency sources
RUN cd /tmp/alsa-lib-1.2.10 && \
    ./configure --enable-shared=yes --enable-static=no --with-pic --host=${TARGET} --prefix=/tmp/deps/${TARGET} && \
    make && make install
RUN cd /tmp/libogg-1.3.5 && \
    ./configure --host=${TARGET} --prefix=/tmp/deps/${TARGET} && \
    make && make install
RUN cd /tmp/libvorbis-1.3.7 && \
    ./configure --host=${TARGET} --prefix=/tmp/deps/${TARGET} && \
    make && make install


WORKDIR /src
ARG GOARCH=amd64 GOAMD64=v1 GOARM=6 GOOUTSUFFIX=-x86_64
ENV CGO_ENABLED=1 PKG_CONFIG_PATH=/tmp/deps/${TARGET}/lib/pkgconfig/ CC=${CC} \
    GOARCH=${GOARCH} GOAMD64=${GOAMD64} GOARM=${GOARM} GOOUTSUFFIX=${GOOUTSUFFIX} \
    GOCACHE=/src/.gocache/go-build GOMODCACHE=/src/.gocache/mod
CMD go build \
    -ldflags="-s -w" \
    -o ./go-librespot${GOOUTSUFFIX} -a ./cmd/daemon

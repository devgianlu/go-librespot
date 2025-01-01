FROM alpine:3.20 AS build

RUN apk -U --no-cache add go alsa-lib-dev libogg-dev libvorbis-dev

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -v ./cmd/daemon

FROM alpine:3.20

RUN apk -U --no-cache add libpulse avahi libgcc gcompat alsa-lib

COPY --from=build /src/daemon /usr/bin/go-librespot

CMD ["/usr/bin/go-librespot", "--config_dir", "/config"]
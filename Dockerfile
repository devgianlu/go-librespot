FROM alpine:3.23 AS build

RUN apk update && apk -U --no-cache add go alsa-lib-dev libogg-dev libvorbis-dev flac-dev mpg123-dev gcc musl-dev

WORKDIR /src

COPY go.mod go.sum ./

RUN go mod download

COPY . .

# Main go-librespot binary
RUN CGO_ENABLED=1 go build -v -o ./go-librespot ./cmd/daemon

# AudioTape metadata tagger
RUN CGO_ENABLED=0 go build -v -o ./audiotape-tagger ./tools/audiotape-tagger

FROM alpine:3.23

RUN apk update && apk -U --no-cache add \
    libpulse \
    avahi \
    libgcc \
    gcompat \
    alsa-lib \
    vorbis-tools

COPY --from=build /src/go-librespot /usr/bin/go-librespot
COPY --from=build /src/audiotape-tagger /usr/bin/audiotape-tagger

CMD ["/usr/bin/go-librespot", "--config_dir", "/config"]
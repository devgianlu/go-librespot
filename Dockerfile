FROM alpine:3.23 AS build

RUN apk update && apk -U --no-cache add go alsa-lib-dev libogg-dev libvorbis-dev flac-dev gcc musl-dev

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 go build -v ./cmd/daemon

FROM alpine:3.23

RUN apk update && apk -U --no-cache add libpulse avahi libgcc gcompat alsa-lib

COPY --from=build /src/daemon /usr/bin/go-librespot

CMD ["/usr/bin/go-librespot", "--state", "/state", "--config", "/config/config.yaml"]

# go-librespot

Yet another open-source Spotify client, written in Go.

## Trying it out

Create a `config.yml` file containing:

```yaml
device_name: go-librespot
credentials:
  type: interactive
```

Then run (or grab a prebuilt binary from the [releases](https://github.com/devgianlu/go-librespot/releases) page):

```shell
go run ./cmd/daemon
```

Follow the instructions on the console for completing authentication and the new device should appear in your Spotify
Connect devices.

Alternatively, you can use the Zeroconf mode:

```yaml
device_name: go-librespot
credentials:
  type: zeroconf
```

## API

The daemon offers an API to control and/or monitor playback.
To enable this features add the following to your `config.yml` file:

```yaml
server:
  enabled: true
  port: 3678
```

For API documentation see [here](API.md).

## Building

The daemon can be easily built with:

```shell
go build -o go-librespot-daemon ./cmd/daemon
```

To crosscompile for different architectures the `GOOS` and `GOARCH` environment variables can be used.

## Development

To recompile protobuf definitions use:

```shell
protoc --go_out=proto --go_opt module=github.com/devgianlu/go-librespot/proto -I proto proto/*.proto
```
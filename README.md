# go-librespot

Yet another open-source Spotify client, written in Go.

## Trying it out

Create a `config.yml` file containing:

```yaml
device_name: go-librespot
credentials:
  type: user_pass
  user_pass:
    username: "<username>"
    password: "<password>"
```

Then, run:

```shell
go run ./cmd/daemon
```

The new device should appear in your Spotify Connect devices.

## API

The daemon offers an API to control and/or monitor playback.
To enable this features add the `server_port` directive to `config.yml` with the port you'd like to use.

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
protoc --go_out=proto --go_opt module=go-librespot/proto -I proto proto/*.proto
```
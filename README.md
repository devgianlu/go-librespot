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

### CORS support

CORS headers can be added to the API via the `server.allow_origin` field. For example, to allow requests from a
website at `http://129.168.1.1:8080`, the following configuration can be used:

```yaml
server:
  enabled: true
  address: "" # allow connections from anywhere (default is localhost only)
  port: 3678
  allow_origin: 'http://129.168.1.1:8080'
```

This is useful when interacting with the API directly from a webpage. 

## Building

The daemon can be easily built with:

```shell
go build -o go-librespot-daemon ./cmd/daemon
```

To crosscompile for different architectures the `GOOS` and `GOARCH` environment variables can be used.

### Dependencies

You need to have the following installed:

* Go 1.22 or higher
* libogg
* libvorbis
* libasound2

You can install the 3 libraries in Debian (and Ubuntu/Raspbian) using the following command:

    sudo apt-get install libogg-dev libvorbis-dev libasound2-dev

You can install a newer Go version from the [Go website](https://go.dev/dl/).

## Development

To recompile protobuf definitions use:

```shell
protoc --go_out=proto --go_opt module=github.com/devgianlu/go-librespot/proto -I proto proto/*.proto
```
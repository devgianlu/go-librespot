# go-librespot

[![GitHub release](https://img.shields.io/github/release/devgianlu/go-librespot.svg)](https://github.com/devgianlu/go-librespot/releases/latest)
![GitHub branch check runs](https://img.shields.io/github/check-runs/devgianlu/go-librespot/master)
[![Go Report Card](https://goreportcard.com/badge/github.com/devgianlu/go-librespot)](https://goreportcard.com/report/github.com/devgianlu/go-librespot)
[![GitHub License](https://img.shields.io/github/license/devgianlu/go-librespot)](https://github.com/devgianlu/go-librespot/blob/master/LICENSE)

Yet another open-source Spotify Connect compatible client, written in Go.

> go-librespot gives you the freedom to have a Spotify Connect device wherever you want.

## Getting Started

### Using prebuilt binary

To get started you can download prebuilt binaries for
the [latest release](https://github.com/devgianlu/go-librespot/releases/latest).

Development prebuilt binaries are also available
as [GitHub Actions artifacts](https://github.com/devgianlu/go-librespot/blob/249b8fee709e2d08fe9c39a16ad0fc4b737cb967/.github/workflows/release.yml#L62).

### Using Docker

A lightweight Docker image for go-librespot is available
on the [GitHub Container Registry](https://github.com/devgianlu/go-fileshare/pkgs/container/go-fileshare).

An example Docker Compose configuration for PulseAudio is available [here](/docker-compose.pulse.yml).

### Building from source

To build from source the following prerequisites are necessary:

- Go 1.22 or higher
- Libraries: `libogg`, `libvorbis`, `libasound2`

To install Go, download it from the [Go website](https://go.dev/dl/).

To install the required libraries on Debian-based systems (Debian, Ubuntu, Raspbian), use:

```shell
sudo apt-get install libogg-dev libvorbis-dev libasound2-dev
```

Once prerequisites are installed you can clone the repository and run the daemon with:

```shell
go run ./cmd/daemon
```

Details about cross-compiling go-librespot are described [here](/CROSS_COMPILE.md) (thank you @felixstorm).

## Configuration

The default directory for configuration files is `~/.config/go-librespot`. You can change this directory with the
`-config_dir` flag. The configuration directory contains:

- `config.yml`: The main configuration (does not exist by default)
- `state.json`: The player state and credentials
- `lockfile`: A lockfile to prevent running multiple instances on the same configuration

The full configuration schema is available [here](/config_schema.json), only the main options are detailed below.

### Zeroconf mode

This is the default mode. It uses mDNS auto discovery to allow Spotify clients inside the same network to connect to
go-librespot. This is also known as Spotify Connect.

An example configuration (not required) looks like this:

```yaml
zeroconf_enabled: false # Whether to keep the device discoverable at all times, even if authenticated via other means
zeroconf_port: 0 # The port to use for Zeroconf, 0 for random
credentials:
  type: zeroconf
  zeroconf:
    persist_credentials: false # Whether to persist zeroconf user credentials even after disconnecting
```

If `persist_credentials` is `true`, after connecting to the device for the first time credentials will be stored locally
and you can switch to interactive mode without having to authenticate manually.

### Interactive mode

This mode allows you to associate your account with the device and make it discoverable even outside the network. It
requires some manual steps to complete the authentication:

1. Configure interactive mode

    ```yaml
    zeroconf_enabled: false # Whether to keep the device discoverable at all times
    credentials:
      type: interactive
    ```

2. Start the daemon to begin the authentication flow
3. Open the authorization link in your browser and wait to be redirected
4. If go-librespot is running on the same device as your browser, authentication should complete successfully
5. (optional) If go-librespot is running on another device, you'll need to copy the URL from your browser and call it
   from the device, for example:

   ```bash
   curl http://127.0.0.1:36842/login?code=xxxxxxxx
   ```

### API server

Optionally, an API server can be started to control and monitor the player. To enable this feature, add the following to
your configuration:

```yaml
server:
  enabled: true
  address: localhost # Which address to bind to
  port: 3678 # The server port
  allow_origin: '' # Value for the Access-Control-Allow-Origin header
  cert_file: '' # Path to certificate file for TLS
  key_file: '' # Path to key file for TLS
```

For detailed API documentation see [here](/API.md).

### Volume synchronization

Various configurations for volume control are available:

1. **No external volume without mixer**: Spotify volume is controlled independently of the device volume, output samples
   are multiplied with the volume
2. **No external volume with mixer**: Spotify volume is synchronized with the device volume and vice versa, output
   samples
   are not volume dependant, Spotify volume changes are applied to the ALSA mixer and vice versa
3. **External volume without mixer**: Spotify volume is not synchronized with the device volume, output samples are not
   volume dependant
4. **External volume with mixer**: Device volume is synchronized with Spotify volume, output samples are not volume
   dependant, volume changes are not applied to the ALSA mixer

### Additional configuration

The following options are also available:

```yaml
log_level: info # Log level configuration (trace, debug, info, warn, error)
device_id: '' # Spotify device ID (auto-generated)
device_name: '' # Spotify device name
device_type: computer # Spotify device type (icon)
audio_backend: alsa # Audio backend to use (alsa, pipe, pulseaudio)
audio_device: default # ALSA audio device to use for playback
mixer_device: '' # ALSA mixer device for volume synchronization 
mixer_control_name: Master # ALSA mixer control name for volume synchronization
audio_buffer_time: 500000 # Audio buffer time in microseconds, ALSA only
audio_period_count: 4 # Number of periods to request, ALSA only
audio_output_pipe: '' # Path to a named pipe for audio output
audio_output_pipe_format: s16le # Audio output pipe format (s16le, s32le, f32le)
bitrate: 160 # Playback bitrate (96, 160, 320)
volume_steps: 100 # Volume steps count
initial_volume: 100 # Initial volume in steps (not applied to the mixer device)
external_volume: false # Whether volume is controlled externally 
disable_autoplay: false # Whether autoplay of more songs should be disabled
```

Make sure to check [here](/config_schema.json) for the full list of options.

## Development

Protobuf definitions are managed through [Buf](https://buf.build). To recompile, execute:

```shell
buf generate
```

or using Go:

```shell
go generate ./...
```
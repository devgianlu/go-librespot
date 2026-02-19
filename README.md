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
on the [GitHub Container Registry](https://github.com/devgianlu/go-librespot/pkgs/container/go-librespot).

An example Docker Compose configuration for PulseAudio is available [here](/docker-compose.pulse.yml).

### Using Brew

You can also install go-librespot [using Brew](https://formulae.brew.sh/formula/go-librespot)
on macOS and Linux (thanks @kriive):

```shell
brew install go-librespot
```

### Building from source

To build from source the following prerequisites are necessary:

- Go 1.25 or higher
- Libraries: `libogg`, `libvorbis`, `flac`, `libasound2`

To install Go, download it from the [Go website](https://go.dev/dl/).

To install the required libraries on Debian-based systems (Debian, Ubuntu, Raspbian), use:

```shell
sudo apt-get install libogg-dev libvorbis-dev libflac-dev libasound2-dev
```

Once prerequisites are installed you can clone the repository and run the daemon with:

```shell
go run ./cmd/daemon
```

Details about cross-compiling go-librespot are described [here](/CROSS_COMPILE.md) (thank you @felixstorm).

## Configuration

The main configuration files is `~/.config/go-librespot/config.yaml`. On macOS devices, this is
`~/Library/Application Support/go-librespot/config.yaml`. It does not exist by default.

You can change this path with the `--config` flag.

State files are stored in `~/.local/state/go-librespot` (`~/Library/Application Support/go-librespot` on macOS).

You can change this directory with the `--state` flag.

The state directory contains:
- `state.json`: The player state and credentials
- `lockfile`: A lockfile to prevent multiple instances using the same state

The full configuration schema is available [here](/config_schema.json), only the main options are detailed below.

### Zeroconf Mode and mDNS Backend Selection

Zeroconf mode enables mDNS auto discovery, allowing Spotify clients inside the same network to connect to go-librespot. This is also known as Spotify Connect.

**Backend selection:**  
go-librespot supports two different backends for mDNS service registration:

- **builtin**: (default) Uses the built-in mDNS responder provided by go-librespot itself.  
- **avahi**: Uses the system's avahi-daemon (via D-Bus) for mDNS service registration.

You can configure which backend to use via the `zeroconf_backend` setting in your configuration file:

```yaml
zeroconf_backend: avahi   # Options: "builtin" (default), "avahi"
```

Or via the command line:

```shell
go-librespot -c zeroconf_backend=avahi
```

#### Which backend should I use?

- Use **avahi** if you want to integrate with an existing Avahi daemon, e.g. on embedded systems, to avoid port conflicts, or to centralize mDNS advertisements with system service management (e.g., using `systemd`).
    - Compatible with Avahi 0.6.x and later (tested with 0.7 and 0.8).
- Use **builtin** if you do **not** have Avahi running and want go-librespot to manage its own mDNS advertisements (no extra dependencies required).

#### Example minimal Zeroconf configuration

```yaml
zeroconf_enabled: true # Whether to keep the device discoverable at all times, even if authenticated via other means
zeroconf_port: 0       # The port to use for Zeroconf, 0 for random
zeroconf_backend: avahi
credentials:
  type: zeroconf
  zeroconf:
    persist_credentials: false # Whether to persist zeroconf user credentials even after disconnecting
```

If `persist_credentials` is `true`, after connecting to the device for the first time credentials will be stored locally
and you can switch to interactive mode without having to authenticate manually.

If `zeroconf_interfaces_to_advertise` is provided, you can limit interfaces that will be advertised. For example, if you
have Docker installed on your host, you may want to disable advertising to its bridge interface, or you may want to
disable interfaces that will not be reachable.

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
  image_size: 'default' # Album art image size (default, small, large, xlarge)
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

### Audio normalization

go-librespot implements audio normalization according to Spotify's standards, which targets **-14 dB LUFS** (Loudness
Units relative to Full Scale) based on the **ITU-R BS.1770** standard.
[Source](https://support.spotify.com/us/artists/article/loudness-normalization/)

Normalization can be configured with the following options:

```yaml
normalisation_disabled: false # Whether to disable normalization (default: false)
normalisation_use_album_gain: false # Whether to use album gain instead of track gain (default: false)
normalisation_pregain: 0 # Pregain in dB to apply before normalization (default: 0)
```

The pregain is applied on Spotify's -14 dB LUFS target. Spotify suggests the following presets for pregain:

- Loud: -11 dB LUFS, apply a pregain of +3 dB
- Normal: -14 dB LUFS, apply a pregain of 0 dB
- Quiet: -19 dB LUFS, apply a pregain of -5 dB

### Additional configuration

The following options are also available:

```yaml
log_level: info # Log level configuration (trace, debug, info, warn, error)
log_disable_timestamp: false # Whether to disable timestamps in log output
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
ignore_last_volume: false # Whether to ignore the last saved volume and always use initial_volume
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

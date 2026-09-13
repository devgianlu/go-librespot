# Contributing to go-librespot

There just a few guidelines to follow when contributing to this project. Please take a moment to read them:

* Try to split your changes into separate, atomic commits that each make one change. This makes it easier to review and
  understand the changes.
* Use [conventional commit messages](https://www.conventionalcommits.org/en/v1.0.0/): the first line of the commit
  message should be the intent followed by a short description of the change (e.g. `feat: added x`), and the body
  should provide more detail, if appropriate.
* Follow Golang conventions for naming, formatting, and structuring code. Use `gofmt` to format your code.
* If you're adding a new feature, please consider opening an issue first to discuss it. This can save you time if the
  feature is not something that can be merged into the project.

## Testing on macOS

Install Go and the native decoder dependencies, then run the same test tags as CI:

```sh
brew install go libogg libvorbis flac mpg123 pkgconf
go test -count=1 -tags "test_unit test_integration" ./...
go build -o /tmp/go-librespot-test ./cmd/daemon
```

The AudioToolbox device test requires a Mac with a working default output device.
It is skipped unless explicitly enabled, so the regular test suite can run without
audio hardware. To test the actual native queue with the race detector and strict
cgo pointer checks:

```sh
GO_LIBRESPOT_TEST_AUDIO_DEVICE=1 GOEXPERIMENT=cgocheck2 \
  go test -race -tags test_unit ./output -run TestAudioToolbox -count=5 -v
```

This submits generated silence at 44.1 kHz and 48 kHz to the default output. It
checks native initial mute and volume changes, callback progress after garbage
collection, three pause/resume cycles per output, and repeated disposal. Pausing
must stop reader consumption; resuming must restart it; no further reads may
occur after disposal. The error-delivery test also runs without an audio device.

These tests do not need Spotify credentials. They verify the local audio queue,
not audible output or successful Spotify authentication, licensing, or track
playback. Report those separately when validating end-to-end playback.

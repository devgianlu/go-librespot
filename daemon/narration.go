package daemon

import (
	"context"
	"strings"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/player"
	narrationpb "github.com/devgianlu/go-librespot/proto/spotify/narration"
)

// A DJ context attaches up to three narrations to a track, all with the same
// shape and each an alternative for a different moment.
const (
	narrationIntroPrefix = "narration.intro"
	narrationJumpPrefix  = "narration.jump"
	narrationOutroPrefix = "narration.outro"
)

// narrationTimeout bounds the synthesis round trip plus the download. Narration
// is a nicety; the track must not be held up for it indefinitely.
const narrationTimeout = 8 * time.Second

// The levels DJ declares for its own narration, used when a clip arrives without
// them.
const (
	spotifyNarrationLoudness = -16.0
	spotifyNarrationTruePeak = -3.0
)

// narrationKinds names the narrations a track carries, in the order they would
// be spoken. Only the ones with a script count: the rest of a narration's
// metadata is present even when there is nothing to say.
func narrationKinds(metadata map[string]string) []string {
	if len(metadata) == 0 {
		return nil
	}

	var kinds []string
	for _, kind := range []struct{ name, prefix string }{
		{"intro", narrationIntroPrefix},
		{"jump", narrationJumpPrefix},
		{"outro", narrationOutroPrefix},
	} {
		if len(metadata[kind.prefix+".ssml"]) > 0 {
			kinds = append(kinds, kind.name)
		}
	}

	return kinds
}

// narrationPlan names which of them will actually be spoken for this load,
// given that intro and jump are alternatives for the same moment.
func narrationPlan(metadata map[string]string, introPrefix string) string {
	var playing []string

	if len(metadata[introPrefix+".ssml"]) > 0 {
		playing = append(playing, strings.TrimPrefix(introPrefix, "narration."))
	}
	if len(metadata[narrationOutroPrefix+".ssml"]) > 0 {
		playing = append(playing, "outro")
	}

	if len(playing) == 0 {
		return "none"
	}

	return strings.Join(playing, " + ")
}

// narrationFor builds one of a track's narration clips, or nil when the track
// has no narration of that kind.
//
// Any failure returns nil rather than an error: losing the DJ's line is a much
// smaller problem than losing the music, so playback always continues.
func (p *AppPlayer) narrationFor(ctx context.Context, metadata map[string]string, uri, prefix string) librespot.AudioSource {
	if len(metadata) == 0 {
		return nil
	}

	ssml := metadata[prefix+".ssml"]
	if len(ssml) == 0 {
		return nil
	}

	log := p.app.log.WithField("uri", uri)

	loudness, truePeak, ok := player.NarrationLoudness(metadata, prefix)
	if !ok {
		// Without a declared loudness the clip would be normalised against a
		// meaningless 0 LUFS, so leave it at the level it was rendered.
		loudness, truePeak = spotifyNarrationLoudness, spotifyNarrationTruePeak
		log.Debugf("narration has no loudness metadata, assuming %g LUFS", loudness)
	}

	ctx, cancel := context.WithTimeout(ctx, narrationTimeout)
	defer cancel()

	req := &narrationpb.TtsRequest{
		Prompt:       &narrationpb.TtsRequest_Ssml{Ssml: ssml},
		AudioFormat:  narrationpb.ResolveRequest_MP3,
		TtsVoice:     narrationVoice(metadata, prefix),
		TtsProvider:  narrationProvider(metadata, prefix),
		SampleRateHz: player.SampleRate,
	}

	started := time.Now()

	audioUrl, err := p.sess.Spclient().NarrationUrl(ctx, req)
	if err != nil {
		log.WithError(err).Warnf("failed resolving narration audio, skipping it")
		return nil
	}

	source, err := player.NewNarrationSource(ctx, p.app.log, p.app.client, audioUrl,
		loudness, truePeak, p.app.cfg.NormalisationPregain)
	if err != nil {
		log.WithError(err).Warnf("failed loading narration audio, skipping it")
		return nil
	}

	log.Debugf("%s ready in %dms", prefix, time.Since(started).Milliseconds())
	return source
}

// narrate wraps a track's audio with the DJ's lines, or returns it unchanged
// when the track has none.
//
// This runs at prefetch time as well as at load time: the player promotes the
// prefetched source the moment the previous one ends, so a bare track there
// would be heard for as long as the synthesis takes.
func (p *AppPlayer) narrate(ctx context.Context, metadata map[string]string, uri string,
	source librespot.AudioSource, introPrefix string) librespot.AudioSource {
	intro := p.narrationFor(ctx, metadata, uri, introPrefix)
	outro := p.narrationFor(ctx, metadata, uri, narrationOutroPrefix)

	if intro == nil && outro == nil {
		return source
	}

	return player.NewNarratedSource(p.app.log, intro, source, outro)
}

func narrationVoice(metadata map[string]string, prefix string) narrationpb.ResolveRequest_TtsVoice {
	name := metadata[prefix+".voice"]
	if v, ok := narrationpb.ResolveRequest_TtsVoice_value[name]; ok {
		return narrationpb.ResolveRequest_TtsVoice(v)
	}

	return narrationpb.ResolveRequest_VOICE1
}

func narrationProvider(metadata map[string]string, prefix string) narrationpb.ResolveRequest_TtsProvider {
	name := metadata[prefix+".tts_provider"]
	if v, ok := narrationpb.ResolveRequest_TtsProvider_value[name]; ok {
		return narrationpb.ResolveRequest_TtsProvider(v)
	}

	return narrationpb.ResolveRequest_SONANTIC_FAST
}

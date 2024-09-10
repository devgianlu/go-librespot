package dealer

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"

	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	log "github.com/sirupsen/logrus"
)

type messageReceiver struct {
	uriPrefixes []string
	c           chan Message
}

type Message struct {
	Uri     string
	Headers map[string]string
	Payload []byte
}

type requestReceiver struct {
	c chan Request
}

type Request struct {
	resp chan bool

	MessageIdent string
	Payload      RequestPayload
}

func (req Request) Reply(success bool) {
	req.resp <- success
}

type RequestPayload struct {
	MessageId      uint32 `json:"message_id"`
	SentByDeviceId string `json:"sent_by_device_id"`
	Command        struct {
		Endpoint         string                    `json:"endpoint"`
		SessionId        string                    `json:"session_id"`
		Data             []byte                    `json:"data"`
		Value            interface{}               `json:"value"`
		Position         int64                     `json:"position"`
		Relative         string                    `json:"relative"`
		Context          *connectpb.Context        `json:"context"`
		PlayOrigin       *connectpb.PlayOrigin     `json:"play_origin"`
		Track            *connectpb.ContextTrack   `json:"track"`
		PrevTracks       []*connectpb.ContextTrack `json:"prev_tracks"`
		NextTracks       []*connectpb.ContextTrack `json:"next_tracks"`
		RepeatingTrack   *bool                     `json:"repeating_track"`
		RepeatingContext *bool                     `json:"repeating_context"`
		ShufflingContext *bool                     `json:"shuffling_context"`
		LoggingParams    struct {
			CommandInitiatedTime int64    `json:"command_initiated_time"`
			PageInstanceIds      []string `json:"page_instance_ids"`
			InteractionIds       []string `json:"interaction_ids"`
			DeviceIdentifier     string   `json:"device_identifier"`
		} `json:"logging_params"`
		Options struct {
			RestorePaused       string `json:"restore_paused"`
			RestorePosition     string `json:"restore_position"`
			RestoreTrack        string `json:"restore_track"`
			AlwaysPlaySomething bool   `json:"always_play_something"`
			SkipTo              struct {
				TrackUid   string `json:"track_uid"`
				TrackUri   string `json:"track_uri"`
				TrackIndex int    `json:"track_index"`
			} `json:"skip_to"`
			InitiallyPaused       bool                                    `json:"initially_paused"`
			SystemInitiated       bool                                    `json:"system_initiated"`
			PlayerOptionsOverride *connectpb.ContextPlayerOptionOverrides `json:"player_options_override"`
			Suppressions          *connectpb.Suppressions                 `json:"suppressions"`
			PrefetchLevel         string                                  `json:"prefetch_level"`
			AudioStream           string                                  `json:"audio_stream"`
			SessionId             string                                  `json:"session_id"`
			License               string                                  `json:"license"`
		} `json:"options"`
		PlayOptions struct {
			OverrideRestrictions bool   `json:"override_restrictions"`
			OnlyForLocalDevice   bool   `json:"only_for_local_device"`
			SystemInitiated      bool   `json:"system_initiated"`
			Reason               string `json:"reason"`
			Operation            string `json:"operation"`
			Trigger              string `json:"trigger"`
		} `json:"play_options"`
		FromDeviceIdentifier string `json:"from_device_identifier"`
	} `json:"command"`
}

func handleTransferEncoding(headers map[string]string, data []byte) ([]byte, error) {
	if transEnc, ok := headers["Transfer-Encoding"]; ok {
		switch transEnc {
		case "gzip":
			gz, err := gzip.NewReader(bytes.NewReader(data))
			if err != nil {
				return nil, fmt.Errorf("invalid gzip stream: %w", err)
			}

			defer func() { _ = gz.Close() }()

			data, err = io.ReadAll(gz)
			if err != nil {
				return nil, fmt.Errorf("failed decompressing gzip payload: %w", err)
			}
		default:
			return nil, fmt.Errorf("unsupported transfer encoding: %s", transEnc)
		}

		delete(headers, "Transfer-Encoding")
	}

	return data, nil
}

func (d *Dealer) handleMessage(rawMsg *RawMessage) {
	//goland:noinspection GoImportUsedAsName
	log := log.WithField("uri", rawMsg.Uri)

	if len(rawMsg.Payloads) > 1 {
		panic("unsupported number of payloads")
	}

	var matchedReceivers []messageReceiver

	// lookup receivers that want to match this message
	d.messageReceiversLock.RLock()
	for _, recv := range d.messageReceivers {
		for _, uriPrefix := range recv.uriPrefixes {
			if strings.HasPrefix(rawMsg.Uri, uriPrefix) {
				matchedReceivers = append(matchedReceivers, recv)
				break
			}
		}
	}
	d.messageReceiversLock.RUnlock()

	if len(matchedReceivers) == 0 {
		log.Debug("skipping dealer message")
		return
	}

	var payloadBytes []byte
	if len(rawMsg.Payloads) > 0 {
		var err error
		switch payload := rawMsg.Payloads[0].(type) {
		case string:
			payloadBytes, err = base64.StdEncoding.DecodeString(payload)
			if err != nil {
				log.WithError(err).Error("invalid base64 payload")
				return
			}
		case []byte:
			payloadBytes = payload
		default:
			log.Warnf("unsupported payload format: %s", reflect.TypeOf(rawMsg.Payloads[0]))
			return
		}

		payloadBytes, err = handleTransferEncoding(rawMsg.Headers, payloadBytes)
		if err != nil {
			log.WithError(err).Errorf("failed decoding message transfer encoding")
			return
		}
	}

	msg := Message{
		Uri:     rawMsg.Uri,
		Headers: rawMsg.Headers,
		Payload: payloadBytes,
	}

	for _, recv := range matchedReceivers {
		recv.c <- msg
	}
}

func (d *Dealer) ReceiveMessage(uriPrefixes ...string) <-chan Message {
	if len(uriPrefixes) == 0 {
		panic("uri prefixes list cannot be empty")
	}

	d.messageReceiversLock.Lock()
	defer d.messageReceiversLock.Unlock()

	// create new receiver
	c := make(chan Message)
	d.messageReceivers = append(d.messageReceivers, messageReceiver{uriPrefixes, c})

	// start receiving if necessary
	d.startReceiving()

	return c
}

func (d *Dealer) handleRequest(rawMsg *RawMessage) {
	//goland:noinspection GoImportUsedAsName
	log := log.WithField("uri", rawMsg.MessageIdent)

	d.requestReceiversLock.RLock()
	recv, ok := d.requestReceivers[rawMsg.MessageIdent]
	d.requestReceiversLock.RUnlock()

	if !ok {
		log.Warn("ignoring dealer request")
		return
	}

	payloadBytes, err := handleTransferEncoding(rawMsg.Headers, rawMsg.Payload.Compressed)
	if err != nil {
		log.WithError(err).Errorf("failed decoding request transfer encoding")
		return
	}

	var payload RequestPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		log.WithError(err).Error("failed unmarshalling dealer request payload")
		return
	}

	// dispatch request
	resp := make(chan bool)
	recv.c <- Request{
		resp:         resp,
		MessageIdent: rawMsg.MessageIdent,
		Payload:      payload,
	}

	// wait for response and send it
	success := <-resp
	if err := d.sendReply(rawMsg.Key, success); err != nil {
		log.WithError(err).Error("failed sending dealer reply")
		return
	}
}

func (d *Dealer) ReceiveRequest(uri string) <-chan Request {
	d.requestReceiversLock.Lock()
	defer d.requestReceiversLock.Unlock()

	// check that there isn't another receiver for this uri
	_, ok := d.requestReceivers[uri]
	if ok {
		panic(fmt.Sprintf("cannot have more request receivers for %s", uri))
	}

	// create new receiver
	c := make(chan Request)
	d.requestReceivers[uri] = requestReceiver{c}

	// start receiving if necessary
	d.startReceiving()

	return c
}

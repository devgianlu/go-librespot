package go_librespot

import (
	"encoding/json"
	"sync"
)

type AppState struct {
	sync.Mutex

	DeviceId     string          `json:"device_id"`
	EventManager json.RawMessage `json:"event_manager"`
	Credentials  struct {
		Username string `json:"username"`
		Data     []byte `json:"data"`
	} `json:"credentials"`
	LastVolume *uint32 `json:"last_volume"`
}

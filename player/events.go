package player

type EventType int

const (
	EventTypePlaying EventType = iota
	EventTypeBuffering
	EventTypePaused
)

type Event struct {
	Type EventType
}

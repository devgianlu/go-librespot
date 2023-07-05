package player

type EventType int

const (
	EventTypePlaying EventType = iota
	EventTypeBuffering
	EventTypePaused
	EventTypeStopped
)

type Event struct {
	Type EventType
}

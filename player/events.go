package player

type EventType int

const (
	EventTypePlaying EventType = iota
	EventTypePaused
	EventTypeStopped
	EventTypeNotPlaying
)

type Event struct {
	Type EventType
}

type EventManager interface {
}

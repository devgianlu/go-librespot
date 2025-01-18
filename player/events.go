package player

type EventType int

const (
	EventTypePlay EventType = iota
	EventTypeResume
	EventTypePause
	EventTypeStop
	EventTypeNotPlaying
)

type Event struct {
	Type EventType
}

type EventManager interface {
}

package goa11y

// EventKind classifies an accessibility event.
type EventKind string

const (
	EventFocus       EventKind = "focus"
	EventStateChange EventKind = "state_change"
	EventNameChange  EventKind = "name_change"
	EventValueChange EventKind = "value_change"
	EventCreate      EventKind = "create"
	EventDestroy     EventKind = "destroy"
	EventSelection   EventKind = "selection"
	EventAlert       EventKind = "alert"
	EventWindow      EventKind = "window"
)

// Event is one accessibility event.
type Event struct {
	Kind   EventKind
	Target string
	Detail string
}

// EventProvider is optionally implemented by backends that can stream
// accessibility events. Subscribe returns a channel that the backend closes
// when the subscription ends.
type EventProvider interface {
	Subscribe() (<-chan Event, error)
}

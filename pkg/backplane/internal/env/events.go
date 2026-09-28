package env

import (
	"context"
	"maps"
	"time"
)

// Message is one event to publish.
type Message struct {
	Event   string // full name "<service>.<Event>"
	Key     string // CloudEvents subject, optional
	ID      string // ce-id and the dedup id; empty: a new UUID
	Time    time.Time
	Payload []byte // encoded JSON
	// Extensions are CloudEvents extension attributes by name, without the
	// ce- prefix; the event package validates the names.
	Extensions map[string]string
}

// Incoming is the metadata of the event a reactor's handler is running
// for; the transport puts it on the handler's context.
type Incoming struct {
	ID         string
	Source     string
	Type       string
	Subject    string
	Time       time.Time
	Attempt    int
	Consumer   string
	Extensions map[string]string
}

type incomingKey struct{}

// WithIncoming returns ctx carrying in.
func WithIncoming(ctx context.Context, in Incoming) context.Context {
	return context.WithValue(ctx, incomingKey{}, in)
}

// IncomingOf returns the metadata WithIncoming put on ctx; the
// extensions are a copy.
func IncomingOf(ctx context.Context) (Incoming, bool) {
	in, ok := ctx.Value(incomingKey{}).(Incoming)
	if ok && in.Extensions != nil {
		in.Extensions = maps.Clone(in.Extensions)
	}

	return in, ok
}

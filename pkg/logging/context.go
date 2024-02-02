package logging

import (
	"context"

	"github.com/rs/zerolog"
)

var eventKey struct{}

// WithContextEvent attaches a log event to the context to allow metadata to be collected on this
// event as the context is passed around.
func WithContextEvent(ctx context.Context, event *zerolog.Event) context.Context {
	return context.WithValue(ctx, eventKey, event)
}

// ContextEvent gets an attached event from the context to add metadata and fields to it.
// It's safe to not nil-check the return value as all of the functions defined on `zerolog.Event`
// do the nil-check internally.
func ContextEvent(ctx context.Context) *zerolog.Event {
	event, ok := ctx.Value(eventKey).(*zerolog.Event)
	if !ok {
		return nil
	}
	return event
}

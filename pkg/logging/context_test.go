package logging

import (
	"bytes"
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestContextEvent(t *testing.T) {
	// Create a logger that writes to a buffer so we can inspect it.
	var buffer bytes.Buffer
	logger := zerolog.New(&buffer)

	// Attach an event to the context...
	event := logger.Info()
	ctx := WithContextEvent(context.Background(), event)

	// ... and add fields to it.
	ContextEvent(ctx).Int("test", 42)
	ContextEvent(ctx).Str("test2", "foo")

	// Eventually log it.
	event.Msg("test log!")

	want := `{"level":"info","test":42,"test2":"foo","message":"test log!"}`
	require.JSONEq(t, want, buffer.String())
}

func TestContextEventNilSafety(t *testing.T) {
	require.NotPanics(t, func() {
		ContextEvent(context.Background()).Str("test", "I hope this doesn't panic!")
	})
}

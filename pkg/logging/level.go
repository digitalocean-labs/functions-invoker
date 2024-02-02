package logging

import (
	"fmt"
	"strings"

	"github.com/rs/zerolog"
)

type Level zerolog.Level

// Decode implement `envconfig.Decoder`.
func (l *Level) Decode(s string) error {
	switch strings.ToLower(s) {
	case "debug":
		*l = Level(zerolog.DebugLevel)
	case "info":
		*l = Level(zerolog.InfoLevel)
	case "warn":
		*l = Level(zerolog.WarnLevel)
	case "error":
		*l = Level(zerolog.ErrorLevel)
	default:
		return fmt.Errorf("failed to parse log level %q", s)
	}
	return nil
}

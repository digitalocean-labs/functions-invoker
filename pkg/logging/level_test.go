package logging

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestLevel(t *testing.T) {
	tests := []struct {
		input     string
		want      Level
		wantError bool
	}{{
		input: `DeBuG`,
		want:  Level(zerolog.DebugLevel),
	}, {
		input: `iNFO`,
		want:  Level(zerolog.InfoLevel),
	}, {
		input: `warn`,
		want:  Level(zerolog.WarnLevel),
	}, {
		input: `ERROR`,
		want:  Level(zerolog.ErrorLevel),
	}, {
		input:     ``,
		wantError: true,
	}, {
		input:     `neverheard`,
		wantError: true,
	}}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			var level Level
			err := level.Decode(tt.input)
			if tt.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.want, level)
		})
	}
}

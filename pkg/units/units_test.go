package units

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseByteSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input     string
		want      ByteSize
		wantError bool
	}{{
		input: "1b",
		want:  1 * Byte,
	}, {
		input: "1k",
		want:  1 * Kilobyte,
	}, {
		input: "1m",
		want:  1 * Megabyte,
	}, {
		input: "1g",
		want:  1 * Gigabyte,
	}, {
		input: "1G",
		want:  1 * Gigabyte,
	}, {
		input: "1GiB",
		want:  1 * Gigabyte,
	}, {
		input: "1GB",
		want:  1 * Gigabyte,
	}, {
		input: " 1    m ",
		want:  1 * Megabyte,
	}, {
		input:     "1.52G", // floats are not supported.
		wantError: true,
	}, {
		input:     "",
		wantError: true,
	}, {
		input:     ".1m",
		wantError: true,
	}, {
		input:     "0", // missing unit.
		wantError: true,
	}, {
		input:     "0t", // t is an unknown unit.
		wantError: true,
	}}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("ParseByteSize(%q)", tt.input), func(t *testing.T) {
			got, err := ParseByteSize(tt.input)
			if tt.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.want, got)
		})

		t.Run(fmt.Sprintf("UnmarshallJSON(%q)", tt.input), func(t *testing.T) {
			bs, err := json.Marshal(tt.input)
			require.NoError(t, err, "failed to marshal input into JSON bytes")

			var got ByteSize
			err = json.Unmarshal(bs, &got)
			if tt.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.want, got)
		})

		t.Run(fmt.Sprintf("Decode(%q)", tt.input), func(t *testing.T) {
			var got ByteSize
			err := got.Decode(tt.input)
			if tt.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.want, got)
		})
	}
}

func TestByteSizeString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input ByteSize
		want  string
	}{{
		input: 1 * Byte,
		want:  "1 B",
	}, {
		input: 1024 * Byte,
		want:  "1 KB",
	}, {
		input: 4096 * Megabyte,
		want:  "4 GB",
	}, {
		input: 4097 * Megabyte,
		want:  "4097 MB",
	}}

	for _, tt := range tests {
		assert.Equal(t, tt.want, tt.input.String())
	}
}

func TestByteSizeUnmarshalJSONErrors(t *testing.T) {
	t.Parallel()

	for _, input := range [][]byte{[]byte(`"1"`), []byte(`1`), {}, nil} {
		var got ByteSize
		assert.Error(t, json.Unmarshal(input, &got))
	}
}

func TestGigabyteSeconds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		memory   ByteSize
		duration time.Duration
		want     float64
	}{{
		memory:   1 * Gigabyte,
		duration: 0 * time.Second,
		want:     0,
	}, {
		memory:   1 * Gigabyte,
		duration: 1 * time.Millisecond,
		want:     0.1,
	}, {
		memory:   1 * Gigabyte,
		duration: 99 * time.Millisecond,
		want:     0.1,
	}, {
		memory:   1 * Gigabyte,
		duration: 100 * time.Millisecond,
		want:     0.1,
	}, {
		memory:   1 * Gigabyte,
		duration: 101 * time.Millisecond,
		want:     0.2,
	}, {
		memory:   1 * Gigabyte,
		duration: 1 * time.Second,
		want:     1,
	}, {
		memory:   128 * Megabyte,
		duration: 1 * time.Second,
		want:     0.125,
	}, {
		memory:   128 * Megabyte,
		duration: 100 * time.Millisecond,
		want:     0.0125,
	}, {
		memory:   128 * Megabyte,
		duration: 123 * time.Millisecond,
		want:     0.025,
	}, {
		memory:   128 * Megabyte,
		duration: 1 * time.Microsecond,
		want:     0.0125,
	}, {
		memory:   128 * Megabyte,
		duration: 1 * time.Nanosecond,
		want:     0.0125,
	}}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("GigabyteSeconds(%s, %s)", tt.memory, tt.duration), func(t *testing.T) {
			require.Equal(t, tt.want, GigabyteSeconds(tt.memory, tt.duration))
		})
	}
}

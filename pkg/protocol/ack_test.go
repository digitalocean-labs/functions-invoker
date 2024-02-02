package protocol

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestMiniCombinedCompletionMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		instance      int
		tid           string
		tidStart      time.Time
		aid           string
		isSystemError bool
		want          string
	}{{
		instance:      1,
		tid:           "testtid",
		tidStart:      time.UnixMilli(153),
		aid:           "testaid",
		isSystemError: false,
		want:          `{"transid":["testtid",153],"response":"testaid","isSystemError":false,"invoker":{"instance":1,"userMemory":"0 g"}}`,
	}, {
		instance:      3,
		tid:           "testtid",
		tidStart:      time.UnixMilli(246),
		aid:           "testaid",
		isSystemError: true,
		want:          `{"transid":["testtid",246],"response":"testaid","isSystemError":true,"invoker":{"instance":3,"userMemory":"0 g"}}`,
	}}

	for _, tt := range tests {
		assert.JSONEq(t, tt.want, string(MiniCombinedCompletionMessage(tt.instance, tt.tid, tt.tidStart, tt.aid, tt.isSystemError)))
	}
}

func TestCombinedCompletionMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		instance      int
		tid           string
		tidStart      time.Time
		activation    []byte
		isSystemError bool
		want          string
	}{{
		instance:      1,
		tid:           "testtid",
		tidStart:      time.UnixMilli(153),
		activation:    []byte(`{"just":"testing"}`),
		isSystemError: false,
		want:          `{"transid":["testtid",153],"response":{"just":"testing"},"isSystemError":false,"invoker":{"instance":1,"userMemory":"0 g"}}`,
	}, {
		instance:      3,
		tid:           "testtid",
		tidStart:      time.UnixMilli(246),
		activation:    []byte(`{"just":"testing"}`),
		isSystemError: true,
		want:          `{"transid":["testtid",246],"response":{"just":"testing"},"isSystemError":true,"invoker":{"instance":3,"userMemory":"0 g"}}`,
	}}

	for _, tt := range tests {
		assert.JSONEq(t, tt.want, string(CombinedCompletionMessage(tt.instance, tt.tid, tt.tidStart, tt.activation, tt.isSystemError)))
	}
}

package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/iotest"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"invoker/pkg/entities"
	"invoker/pkg/protocol"
)

func TestRunnerInvoke(t *testing.T) {
	t.Parallel()

	// Activations to be "returned" by the inner runner.
	successfulActivation := &entities.Activation{
		Response: entities.ActivationResponse{
			StatusCode: entities.ActivationStatusCodeSuccess,
		},
	}
	successfulActivationBs, err := json.Marshal(successfulActivation)
	require.NoError(t, err)

	tests := []struct {
		name       string
		input      *http.Request
		wantStatus int
		wantBody   []byte
	}{{
		name: "successful blocking invocation",
		input: func() *http.Request {
			msg := []byte(`
{
	"activationId": "testactivationid",
	"blocking": true,
	"transid": [
		"testtransactionid",
		1337
	]
}
`)
			return httptest.NewRequest(http.MethodPost, "https://foo.bar", bytes.NewReader(msg))
		}(),
		wantStatus: http.StatusOK,
		wantBody:   protocol.CombinedCompletionMessage(42, "testtransactionid", time.UnixMilli(1337), successfulActivationBs, false /*isSystemError*/),
	}, {
		name: "successful non-blocking invocation",
		input: func() *http.Request {
			msg := []byte(`
{
	"activationId": "testactivationid",
	"blocking": false,
	"transid": [
		"testtransactionid",
		1337
	]
}
`)
			return httptest.NewRequest(http.MethodPost, "https://foo.bar", bytes.NewReader(msg))
		}(),
		wantStatus: http.StatusOK,
		wantBody:   protocol.MiniCombinedCompletionMessage(42, "testtransactionid", time.UnixMilli(1337), "testactivationid", false /*isSystemError*/),
	}, {
		name:       "fails to read body",
		input:      httptest.NewRequest(http.MethodPost, "https://foo.bar", iotest.ErrReader(assert.AnError)),
		wantStatus: http.StatusInternalServerError,
	}, {
		name:       "malformed message",
		input:      httptest.NewRequest(http.MethodPost, "https://foo.bar", bytes.NewReader([]byte("foo"))),
		wantStatus: http.StatusBadRequest,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &Runner{
				InstanceID:  42,
				Logger:      zerolog.New(zerolog.NewTestWriter(t)),
				InnerRunner: fakeInnerRunner{activation: successfulActivation},
			}

			resp := httptest.NewRecorder()
			runner.ServeHTTP(resp, tt.input)

			require.Equal(t, tt.wantStatus, resp.Code)
			require.Equal(t, string(tt.wantBody), resp.Body.String())
		})
	}
}

type fakeInnerRunner struct {
	activation *entities.Activation
}

func (f fakeInnerRunner) Invoke(context.Context, zerolog.Logger, *protocol.ActivationMessage) *entities.Activation {
	return f.activation
}

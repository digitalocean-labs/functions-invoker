package entities

import (
	_ "embed"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"invoker/pkg/units"
)

var (
	// This is taken from a "normal" invocation. Annotations have been alphabetically ordered.
	//go:embed testdata/activation_full.json
	activationFull string

	// This is taken from a failed invocation. Annotations have been alphabetically ordered.
	//go:embed testdata/activation_full_with_error.json
	activationFullWithError string

	// This is taken from a remote-build invocation. Annotations have been alphabetically ordered.
	//go:embed testdata/activation_remote_build.json
	activationRemoteBuild string

	// This is taken from a sequenced invocation. Annotations have been alphabetically ordered.
	//go:embed testdata/activation_sequence.json
	activationSequence string
)

func TestActivationMarshal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input Activation
		want  string
	}{{
		name: "full example",
		input: Activation{
			ActivationID: "d0c896443b3a49e68896443b3ae9e632",
			Namespace:    "testnamespace",
			Subject:      "testsubject",
			Annotations: Annotations{
				"thisis": "atest",
			},
			Function: ActivationFunction{
				Name:       "testfunction",
				Version:    "0.0.2",
				Path:       "testnamespace/testfunction",
				Kind:       "nodejs:14",
				Binding:    "abinding",
				Entrypoint: "main",
				Limits: Limits{
					Timeout: 3 * time.Second,
					Memory:  256 * units.Megabyte,
					Logs:    16 * units.Kilobyte,
				},
			},
			Start:     time.UnixMilli(1669716867765),
			End:       time.UnixMilli(1669716867796),
			IsTimeout: false,
			InitTime:  21 * time.Millisecond,
			WaitTime:  203 * time.Millisecond,
			Response: ActivationResponse{
				Result:     []byte(`{"hello":"world"}`),
				StatusCode: ActivationStatusCodeSuccess,
			},
			Logs: []string{
				"2022-11-29T10:14:27.785433679Z stdout: hello world",
			},
		},
		want: activationFull,
	}, {
		name: "full example with developer error",
		input: Activation{
			ActivationID: "d0c896443b3a49e68896443b3ae9e632",
			Namespace:    "testnamespace",
			Subject:      "testsubject",
			Annotations: Annotations{
				"thisis": "atest",
			},
			Function: ActivationFunction{
				Name:       "testfunction",
				Version:    "0.0.2",
				Path:       "testnamespace/testfunction",
				Kind:       "nodejs:14",
				Binding:    "abinding",
				Entrypoint: "main",
				Limits: Limits{
					Timeout: 3 * time.Second,
					Memory:  256 * units.Megabyte,
					Logs:    16 * units.Kilobyte,
				},
			},
			Start:     time.UnixMilli(1669716867765),
			End:       time.UnixMilli(1669716867796),
			IsTimeout: false,
			InitTime:  21 * time.Millisecond,
			WaitTime:  203 * time.Millisecond,
			Response: ActivationResponse{
				Error:      "The function exhausted its memory and was aborted.",
				StatusCode: ActivationStatusCodeDeveloperError,
			},
			Logs: []string{
				"2022-11-29T10:14:27.785433679Z stdout: hello world",
			},
		},
		want: activationFullWithError,
	}, {
		name: "remote build",
		input: Activation{
			ActivationID: "c0593b9885c14477993b9885c1a477da",
			Namespace:    "testnamespace",
			Subject:      "testsubject",
			Function: ActivationFunction{
				Name:       "build_nodejs_18",
				Version:    "0.0.1",
				Path:       "fnsystem/builder/build_nodejs_18",
				Kind:       "nodejs:18",
				Entrypoint: "main",
				Limits: Limits{
					Timeout: 2 * time.Minute,
					Memory:  1 * units.Gigabyte,
					Logs:    16 * units.Kilobyte,
				},
			},
			Start:     time.UnixMilli(1669718340339),
			End:       time.UnixMilli(1669718354725),
			IsTimeout: false,
			InitTime:  451 * time.Millisecond,
			WaitTime:  802 * time.Millisecond,
			Response: ActivationResponse{
				// The rest of the result has been redacted for readability.
				Result:     []byte(`{"outcome":{"actionVersions":{"sample/hello-nodejszip":{"digest":"f28f6882ddf3ba0dd865d22653140efc03dd8a74084e1c4deb69c1135f9cd6f9","version":"0.0.11"}}}}`),
				StatusCode: ActivationStatusCodeSuccess,
			},
			Logs: []string{
				"2022-11-29T10:39:00.353006633Z stdout: cmd: dosls deploy slice:builds/fnsystem/sample_hello-nodejszip/2022-11-29T10-38-57.763Z",
				// The rest of the logs have been redacted for readability.
			},
		},
		want: activationRemoteBuild,
	}, {
		name: "sequence",
		input: Activation{
			ActivationID: "04551f0b0236462e951f0b0236d62e55",
			Namespace:    "testnamespace",
			Subject:      "testsubject",
			Cause:        "146fa7a1f0c94196afa7a1f0c9b19666",
			Function: ActivationFunction{
				Name:       "testfunction",
				Version:    "0.0.1",
				Path:       "testnamespace/testfunction",
				Kind:       "nodejs:14",
				Entrypoint: "main",
				Limits: Limits{
					Timeout: 3 * time.Second,
					Memory:  256 * units.Megabyte,
					Logs:    16 * units.Kilobyte,
				},
			},
			Start:     time.UnixMilli(1669720432240),
			End:       time.UnixMilli(1669720432259),
			IsTimeout: false,
			InitTime:  18 * time.Millisecond,
			// No wait time as its computation is skipped for sequenced invocations.
			Response: ActivationResponse{
				Result:     []byte(`{}`),
				StatusCode: ActivationStatusCodeSuccess,
			},
		},
		want: activationSequence,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.input)
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))
		})
	}
}

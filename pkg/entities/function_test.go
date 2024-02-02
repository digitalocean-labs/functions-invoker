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
	// This is taken from the health functions created by the controller. Generally, we shouldn't
	// be seeing "directly" inlined code anymore but since this path is bypassing a lot of the
	// controller itself, it still has it.
	//go:embed testdata/function_inlined_plain.json
	functionInlinedPlain []byte

	// This happens for very small functions, think a quick single-file Node.js function.
	//go:embed testdata/function_inlined_attachment_plain.json
	functionInlinedAttachmentPlain []byte

	// This happens for very small zips and other binaries. A Node.js function with a small dependency
	// for example.
	//go:embed testdata/function_inlined_attachment_binary.json
	functionInlinedAttachmentBinary []byte

	// This happens for creating functions with an empty file.
	//go:embed testdata/function_inlined_attachment_empty.json
	functionInlinedAttachmentEmpty []byte

	// This happens for very large plaintext files. A Node.js function built with webpack might end
	// up in this case.
	//go:embed testdata/function_external_attachment_plain.json
	functionExternalAttachmentPlain []byte

	// This happens for slightly larger zips and binaries.
	//go:embed testdata/function_external_attachment_binary.json
	functionExternalAttachmentBinary []byte
)

func TestFunctionUnmarshal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input []byte
		want  Function
	}{{
		name:  "function with directly inlined code",
		input: functionInlinedPlain,
		want: Function{
			Name:        "invokerHealthTestAction0",
			Namespace:   "whisk.system",
			Annotations: Annotations{},
			Limits: Limits{
				Timeout: 3 * time.Second,
				Memory:  128 * units.Megabyte,
				Logs:    16 * units.Kilobyte,
			},
			Exec: Exec{
				Kind: "nodejs:14",
				Code: FunctionCode{
					Resolved: `function main(params) { return params; }`,
				},
			},
		},
	}, {
		name:  "function with inlined attachment in plaintext",
		input: functionInlinedAttachmentPlain,
		want: Function{
			Name:      "test",
			Namespace: "testuser",
			Annotations: Annotations{
				"provide-api-key": false,
				"exec":            "nodejs:14",
			},
			Limits: Limits{
				Timeout: 3 * time.Second,
				Memory:  256 * units.Megabyte,
				Logs:    16 * units.Kilobyte,
			},
			Exec: Exec{
				Kind: "nodejs:14",
				Code: FunctionCode{
					Resolved: `function main() { return {} }`,
				},
			},
		},
	}, {
		name:  "function with inlined attachment in binary",
		input: functionInlinedAttachmentBinary,
		want: Function{
			Name:      "test",
			Namespace: "testuser",
			Annotations: Annotations{
				"provide-api-key": false,
				"exec":            "nodejs:14",
			},
			Limits: Limits{
				Timeout: 3 * time.Second,
				Memory:  256 * units.Megabyte,
				Logs:    16 * units.Kilobyte,
			},
			Exec: Exec{
				Kind: "nodejs:14",
				Code: FunctionCode{
					// This example is taken from https://gobyexample.com/base64-encoding as it
					// differs between URLEncoding and StdEncoding making sure we're doing the
					// right thing here (reencoding).
					Resolved: `YWJjMTIzIT8kKiYoKSctPUB+`,
				},
				Binary: true,
			},
		},
	}, {
		name:  "function with empty inlined attachment",
		input: functionInlinedAttachmentEmpty,
		want: Function{
			Name:      "test",
			Namespace: "testuser",
			Annotations: Annotations{
				"provide-api-key": false,
				"exec":            "nodejs:14",
			},
			Limits: Limits{
				Timeout: 3 * time.Second,
				Memory:  256 * units.Megabyte,
				Logs:    16 * units.Kilobyte,
			},
			Exec: Exec{
				Kind: "nodejs:14",
				Code: FunctionCode{
					Resolved: ``,
				},
				Binary: false,
			},
		},
	}, {
		name:  "function with external attachment in plain",
		input: functionExternalAttachmentPlain,
		want: Function{
			Name:      "test",
			Namespace: "testuser",
			Annotations: Annotations{
				"provide-api-key": false,
				"exec":            "nodejs:14",
			},
			Limits: Limits{
				Timeout: 3 * time.Second,
				Memory:  256 * units.Megabyte,
				Logs:    16 * units.Kilobyte,
			},
			Exec: Exec{
				Kind: "nodejs:14",
				Code: FunctionCode{
					AttachmentID: "a184456c-a0b8-4291-8445-6ca0b822917f",
					IsBinary:     false,
				},
				Binary: false,
			},
		},
	}, {
		name:  "function with external attachment in binary",
		input: functionExternalAttachmentBinary,
		want: Function{
			Name:      "test",
			Namespace: "testuser",
			Annotations: Annotations{
				"provide-api-key": false,
				"exec":            "nodejs:14",
			},
			Limits: Limits{
				Timeout: 3 * time.Second,
				Memory:  256 * units.Megabyte,
				Logs:    16 * units.Kilobyte,
			},
			Exec: Exec{
				Kind: "nodejs:14",
				Code: FunctionCode{
					AttachmentID: "50ddbba2-b12b-4a62-9dbb-a2b12bba62c1",
					IsBinary:     true,
				},
				Binary: true,
			},
		},
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got Function
			err := json.Unmarshal(tt.input, &got)
			require.NoError(t, err)

			require.Equal(t, tt.want, got)
		})
	}
}

func TestLimitsMarshalUnmarshal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input Limits
		want  string
	}{{
		name:  "empty",
		input: Limits{},
		want:  `{"timeout":0,"memory":0,"logs":0}`,
	}, {
		name: "everything defined",
		input: Limits{
			Timeout: 1 * time.Minute,
			Memory:  1 * units.Gigabyte,
			Logs:    1 * units.Megabyte,
		},
		want: `{"timeout":60000,"memory":1024,"logs":1024}`,
	}, {
		name: "everything defined but different",
		input: Limits{
			Timeout: 10 * time.Second,
			Memory:  64 * units.Megabyte,
			Logs:    4 * units.Kilobyte,
		},
		want: `{"timeout":10000,"memory":64,"logs":4}`,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.input)
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))

			var got2 Limits
			err = json.Unmarshal(got, &got2)
			require.NoError(t, err)
			require.Equal(t, tt.input, got2)
		})
	}
}

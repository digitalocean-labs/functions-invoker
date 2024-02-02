package exec

import (
	_ "embed"
	"testing"

	"github.com/stretchr/testify/require"

	"invoker/pkg/units"
)

//go:embed testdata/runtimes_full.json
var runtimesFull string

func TestRuntimeManifestsDecode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		want      RuntimeManifests
		wantError bool
	}{{
		name:  "full example",
		input: runtimesFull,
		want: RuntimeManifests{
			"nodejs:14": RuntimeManifest{
				Kind:    "nodejs:14",
				Default: true,
				Image:   "testregistry/action-nodejs-v14:testtag",
				Stemcells: []RuntimeStemcellConfig{{
					Count:  3,
					Memory: 256 * units.Megabyte,
				}},
			},
			"nodejs:default": RuntimeManifest{
				Kind:    "nodejs:14",
				Default: true,
				Image:   "testregistry/action-nodejs-v14:testtag",
				Stemcells: []RuntimeStemcellConfig{{
					Count:  3,
					Memory: 256 * units.Megabyte,
				}},
			},
			"nodejs-lambda:14": RuntimeManifest{
				Kind:    "nodejs-lambda:14",
				Default: false,
				Image:   "testregistry/action-nodejs-v14-lambda:testtag",
			},
			"nodejs:18": RuntimeManifest{
				Kind:    "nodejs:18",
				Default: false,
				Image:   "testregistry/action-nodejs-v18:testtag",
			},
			"nodejs-lambda:18": RuntimeManifest{
				Kind:    "nodejs-lambda:18",
				Default: false,
				Image:   "testregistry/action-nodejs-v18-lambda:testtag",
			},
			"python:3.9": RuntimeManifest{
				Kind:    "python:3.9",
				Default: true,
				Image:   "testregistry/action-python-v3.9:testtag",
			},
			"python:default": RuntimeManifest{
				Kind:    "python:3.9",
				Default: true,
				Image:   "testregistry/action-python-v3.9:testtag",
			},
			"go:1.15": RuntimeManifest{
				Kind:    "go:1.15",
				Default: false,
				Image:   "testregistry/actionloop-golang-v1.15:testtag",
			},
			"go:1.17": RuntimeManifest{
				Kind:    "go:1.17",
				Default: true,
				Image:   "testregistry/actionloop-golang-v1.17:testtag",
			},
			"go:default": RuntimeManifest{
				Kind:    "go:1.17",
				Default: true,
				Image:   "testregistry/actionloop-golang-v1.17:testtag",
			},
			"php:8.0": RuntimeManifest{
				Kind:    "php:8.0",
				Default: true,
				Image:   "testregistry/actionloop-php-v8.0:testtag",
			},
			"php:default": RuntimeManifest{
				Kind:    "php:8.0",
				Default: true,
				Image:   "testregistry/actionloop-php-v8.0:testtag",
			},
		},
	}, {
		name:      "broken JSON",
		input:     "[]",
		wantError: true,
	}, {
		name:      "broken nested JSON",
		input:     `{"runtimes":"foo"}`,
		wantError: true,
	}, {
		name:      "runtimes not present",
		input:     `{"foo":"bar"}`,
		wantError: true,
	}, {
		name:      "duplicate default",
		input:     `{"runtimes":{"nodejs":[{"kind":"nodejs:14","default":true},{"kind":"nodejs:18","default":true}]}}`,
		wantError: true,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var manifests RuntimeManifests
			err := manifests.Decode(tt.input)
			if tt.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.want, manifests)
		})
	}
}

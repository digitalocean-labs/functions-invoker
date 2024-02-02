package exec

import (
	"encoding/json"
	"fmt"

	"invoker/pkg/units"
)

// RuntimeManifests is a collection of all runtimes and their respective settings.
type RuntimeManifests map[string]RuntimeManifest

// Decode implement `envconfig.Decoder`.
func (rm *RuntimeManifests) Decode(s string) error {
	// Parse into a generic structure to drop the top-level "runtimes" key.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return fmt.Errorf("failed to parse top-level runtime manifest as JSON: %w", err)
	}

	// The JSON-structure is keyed by "runtime family", which is not useful for querying...
	var rawManifests map[string][]RuntimeManifest
	if err := json.Unmarshal(raw["runtimes"], &rawManifests); err != nil {
		return fmt.Errorf("failed to parse runtime manifest as JSON: %w", err)
	}

	// ... Instead, we want to query by kind directly.
	manifests := make(RuntimeManifests)
	for familyName, family := range rawManifests {
		for _, version := range family {
			manifests[version.Kind] = version

			// If this version is the current default, resolve $family:default to it as well.
			if version.Default {
				// Check if we already have seen a default version of this runtime and error out
				// if we did.
				defaultName := familyName + ":default"
				if _, ok := manifests[defaultName]; ok {
					return fmt.Errorf("at least two versions of family %q specified as default", familyName)
				}
				manifests[defaultName] = version
			}
		}
	}
	*rm = manifests
	return nil
}

// RuntimeManifest describes a specific version of a runtime, i.e. nodejs:14.
//
// The list of fields is not exhaustive by design. We only parse and deal with fields that matter
// to the invoker.
type RuntimeManifest struct {
	// Kind is the identifier of the runtime, i.e. nodejs:14.
	Kind string `json:"kind"`
	// Default defines if the current version is the default for the family. If this is true, the
	// respective runtime will resolve under the :default tag as well, i.e. nodejs:default.
	Default bool `json:"default"`
	// Image defines which image will be used for this runtime.
	Image RuntimeImage `json:"image"`
	// Stemcells configures how many stemcell containers will be launched for this runtime.
	Stemcells []RuntimeStemcellConfig `json:"stemCells"`
}

// RuntimeImage describes the image to use for a runtime.
type RuntimeImage string

// UnmarshalJSON implements the Golang stdlib unmarshaller.
//
// We parse the JSON-structure into a string directly to avoid having to rebuild it all the time.
// We're not interested in the individual values anyway and only do this for compatibility.
func (i *RuntimeImage) UnmarshalJSON(data []byte) error {
	var runtimeImage struct {
		Prefix string `json:"prefix"`
		Name   string `json:"name"`
		Tag    string `json:"tag"`
	}
	if err := json.Unmarshal(data, &runtimeImage); err != nil {
		return err
	}

	*i = RuntimeImage(fmt.Sprintf("%s/%s:%s", runtimeImage.Prefix, runtimeImage.Name, runtimeImage.Tag))
	return nil
}

// RuntimeStemcellConfig configures the amount and size of stemcell containers for a given runtime.
type RuntimeStemcellConfig struct {
	// Count is the amount of stemcell containers to launch.
	Count int `json:"count"`
	// Memory is the amount of memory to launch the stemcell containers with.
	Memory units.ByteSize `json:"memory"`
}

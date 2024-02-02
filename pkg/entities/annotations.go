package entities

import (
	"encoding/json"
	"sort"
)

// Annotations are key/value pairs describing the respective entity further.
//
// In the API, annotations are, like parameters, a list of objects with key/value and potentially
// (in the case of parameters) additional metadata. Annotations allow us to simplify this to a map
// from string to any without losing fidelity but gaining easy lookup access.
type Annotations map[string]any

// MarshalJSON implements the Golang stdlib marshaller.
func (a Annotations) MarshalJSON() ([]byte, error) {
	list := make([]jsonAnnotation, 0, len(a))
	for k, v := range a {
		list = append(list, jsonAnnotation{Key: k, Value: v})
	}

	// Sort the list by key to make for a predictable outcome as map order isn't guaranteed in Go.
	sort.Slice(list, func(i, j int) bool {
		return list[i].Key < list[j].Key
	})

	return json.Marshal(list)
}

// UnmarshalJSON implements the Golang stdlib unmarshaller.
func (a *Annotations) UnmarshalJSON(data []byte) error {
	var list []jsonAnnotation
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	annos := make(map[string]any, len(list))
	for _, anno := range list {
		annos[anno.Key] = anno.Value
	}

	*a = annos
	return nil
}

// IsTruthy returns true if the given annotation exists and has a value that could be interpreted
// as true. If the annotation doesn't exist, valueForNonExistent is returned.
func (a Annotations) IsTruthy(key string, valueForNonExistent bool) bool {
	val, ok := a[key]
	if !ok {
		return valueForNonExistent
	}

	// This logic is copied as-is from the existing implementation to maintain compatibility.
	switch v := val.(type) {
	case bool:
		return v
	case float64:
		// A float64 check is equivalent to checking numbers as the unmarshaller transforms numbers
		// into floats.
		return v != 0
	case string:
		return v != ""
	case nil:
		return false
	default:
		return true
	}
}

// jsonAnnotation is a key/value-pair as represented in persistence and the API.
type jsonAnnotation struct {
	Key   string `json:"key,omitempty"`
	Value any    `json:"value,omitempty"`
}

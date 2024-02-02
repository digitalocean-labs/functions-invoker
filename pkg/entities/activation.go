package entities

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"invoker/pkg/units"
)

//go:generate mockgen -package mocks -source=activation.go -destination mocks/activation_mock.go

// ActivationWriter can write activations to a potentially external store.
type ActivationWriter interface {
	// WriteActivation writes the activation to a potentially external store.
	WriteActivation(context.Context, *Activation) error
}

// Activation represents an activation as created in the system.
// Some of the annotation-based "quasi-schema" has been inlined into the typed structure and is
// supposed to be deconsolidated when marshalling to the respective destination.
type Activation struct {
	// ActivationID is the ID of the activation.
	ActivationID string
	// Namespace is the namespace of the activation.
	Namespace string
	// Subject is the subject that caused the activation.
	Subject string
	// Cause is the upstream cause of the activation.
	Cause string
	// Annotations are annotations stored with the activation.
	Annotations Annotations
	// Function is metadata about the invoked function.
	Function ActivationFunction

	// Start is the start of the activation.
	Start time.Time
	// End is the end of the activation.
	End time.Time
	// IsTimeout specifies whether or not the activation timed out.
	IsTimeout bool
	// InitTime specifies the time it took to initialize the function, if applicable.
	InitTime time.Duration
	// WaitTime specifies the time it took for the activation to reach the actual invocation of
	// the function, if applicable.
	WaitTime time.Duration

	// Response is the actual response of the activation.
	Response ActivationResponse
	// Logs are log lines written during the activation.
	Logs []string
}

type ActivationFunction struct {
	// Name is the name of the invoked function.
	Name string
	// Version is the version of the invoked function.
	Version string
	// Path is the fully qualified name of the invoked function.
	Path string
	// Kind is the kind of the invoked function.
	Kind string
	// Entrypoint is the entrypoint of the invoked function.
	Entrypoint string
	// Limits are the limits applied to the invoked function.
	Limits Limits
	// Binding is the name of the binding to a package that this function is part of.
	Binding string
}

// ActivationResponse is the response of the activation.
type ActivationResponse struct {
	// Result is the value returned from the function.
	Result json.RawMessage
	// Error is an error that happened when invoking the function. Either Error or Result are set.
	Error string
	// StatusCode determines whether or not the activation was successful.
	StatusCode ActivationStatusCode
}

// GigabyteSeconds computes the gigabyte seconds consumed by the activation, taking into account
// exemptions like the builder functions.
func (a Activation) GigabyteSeconds() float64 {
	if strings.HasPrefix(a.Function.Path, "fnsystem/builder") {
		return 0
	}
	return units.GigabyteSeconds(a.Function.Limits.Memory, a.End.Sub(a.Start))
}

// MarshalJSON implements the Golang stdlib marshaller.
func (a Activation) MarshalJSON() ([]byte, error) {
	if a.Annotations == nil {
		a.Annotations = make(Annotations, 6)
	}

	// Add all of the quasi-schema annotations here.
	a.Annotations["path"] = a.Function.Path
	a.Annotations["kind"] = a.Function.Kind
	a.Annotations["entry"] = a.Function.Entrypoint
	a.Annotations["limits"] = a.Function.Limits
	if a.Function.Binding != "" {
		a.Annotations["binding"] = a.Function.Binding
	}
	a.Annotations["timeout"] = a.IsTimeout
	a.Annotations["gbs"] = a.GigabyteSeconds()

	if a.WaitTime > 0 {
		a.Annotations["waitTime"] = a.WaitTime.Milliseconds()
	}

	if a.InitTime > 0 {
		a.Annotations["initTime"] = a.InitTime.Milliseconds()
	}

	// The parser on the controller side does not tolerate a null field here.
	logs := a.Logs
	if logs == nil {
		logs = []string{}
	}

	result := a.Response.Result
	size := len(a.Response.Result)
	if a.Response.Error != "" {
		errorResult, err := json.Marshal(map[string]string{"error": a.Response.Error})
		if err != nil {
			return nil, fmt.Errorf("failed to marshal error result: %w", err)
		}
		result = errorResult
		size = len(errorResult)
	}

	interim := map[string]any{
		"activationId": a.ActivationID,
		"namespace":    a.Namespace,
		"subject":      a.Subject,
		"name":         a.Function.Name,
		"version":      a.Function.Version,
		"annotations":  a.Annotations,
		"logs":         logs,
		"start":        a.Start.UnixMilli(),
		"end":          a.End.UnixMilli(),
		"duration":     a.End.Sub(a.Start).Milliseconds(),
		"response": map[string]any{
			"result":     result,
			"statusCode": a.Response.StatusCode,
			"size":       size,
		},
		"publish": false,
	}

	if a.Cause != "" {
		interim["cause"] = a.Cause
		a.Annotations["causedBy"] = "sequence"
	}

	return json.Marshal(interim)
}

// ActivationStatusCode describes the status of the activation.
type ActivationStatusCode int

const (
	// ActivationStatusCodeSuccess is an activation that ran successfully.
	ActivationStatusCodeSuccess ActivationStatusCode = 0
	// ActivationStatusCodeApplicationError is an activation that returned an error.
	ActivationStatusCodeApplicationError ActivationStatusCode = 1
	// ActivationStatusCodeDeveloperError is an activation that failed due to a developer error.
	ActivationStatusCodeDeveloperError ActivationStatusCode = 2
	// ActivationStatusCodeDeveloperError is an activation that failed due to a system error.
	ActivationStatusCodeSystemError ActivationStatusCode = 3
)

// String returns a human-readable explanation of the respective status code.
func (a ActivationStatusCode) String() string {
	switch a {
	case ActivationStatusCodeSuccess:
		return "success"
	case ActivationStatusCodeApplicationError:
		return "application error"
	case ActivationStatusCodeDeveloperError:
		return "developer error"
	case ActivationStatusCodeSystemError:
		return "internal error"
	default:
		return ""
	}
}

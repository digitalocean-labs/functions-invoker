package exec

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/valyala/fastjson"
)

// InvocationEnvironment is the environment available to every invocation.
type InvocationEnvironment struct {
	// ActivationID is the activationID of the current invocation.
	ActivationID string
	// TransactionID is the transactionID of the current transaction.
	TransactionID string
	// Namespace is the namespace of the current invocation.
	Namespace string
	// FunctionName is the function that's being invoked.
	FunctionName string
	// FunctionVersion is the version of the function being invoked.
	FunctionVersion string
	// Deadline marks the time at which the current invocation will be considered a timeout.
	Deadline time.Time
	// APIKey is the key with which the function was invoked. Only provided optionally.
	APIKey string
}

// ContainerInitPayload is the payload sent to a container when initializing it with a function.
type ContainerInitPayload struct {
	// Name is the name of the function being invoked.
	// OPTIM: This seems unused by the golang-proxy etc. Maybe it can be dropped.
	Name string
	// Entrypoint is the entrypoint of the function.
	Entrypoint string
	// Code is the code of the function. This can either be plaintext or base64-encoded binary data.
	Code string
	// IsBinary defines whether or not the passed code is in binary format.
	IsBinary bool
	// Environment is the runtime environment of the given initialization.
	Environment InvocationEnvironment
	// Parameters are parameters passed during initialization
	Parameters map[string]json.RawMessage
}

func (e ContainerInitPayload) MarshalJSON() ([]byte, error) {
	env := map[string]string{
		"__OW_ACTIVATION_ID":  e.Environment.ActivationID,
		"__OW_TRANSACTION_ID": e.Environment.TransactionID,
		"__OW_NAMESPACE":      e.Environment.Namespace,
		"__OW_ACTION_NAME":    e.Environment.FunctionName,
		"__OW_ACTION_VERSION": e.Environment.FunctionVersion,
		"__OW_DEADLINE":       fmt.Sprint(e.Environment.Deadline.UnixMilli()), // Needs to be a string!
	}
	if e.Environment.APIKey != "" {
		env["__OW_API_KEY"] = e.Environment.APIKey
	}

	// InitParameters are reflected as environment variables, which are only strings.
	for k, v := range e.Parameters {
		if len(v) > 0 && v[0] == '"' {
			// This is a string! We have to parse it to handle escaped character correctly.
			val, err := fastjson.ParseBytes(v)
			if err != nil {
				return nil, fmt.Errorf("failed to parse init parameter %s into string: %w", k, err)
			}
			env[k] = string(val.GetStringBytes())
			continue
		}

		// Everything that's not a string can just be stringified as-is.
		env[k] = string(v)
	}

	return json.Marshal(map[string]any{
		"value": map[string]any{
			"name":   e.Name,
			"main":   e.Entrypoint,
			"code":   e.Code,
			"binary": e.IsBinary,
			"env":    env,
		},
	})
}

// ContainerRunPayload is the payload sent to a container when running a function.
type ContainerRunPayload struct {
	// Environment are the generic environment fields for both init and run payloads.
	Environment InvocationEnvironment
	// Parameters are all the parameters passed during runtime of the function.
	Parameters map[string]json.RawMessage
}

// MarshalJSON implements the Golang stdlib marshaller.
// OPTIM: Note how these are the same keys as above, but lowercases and without the __OW_ prefix.
// There's an inconsistency here where the runtime takes init's env as-is but transforms the run's
// env to be the same as the init env internally. This might be worth fixing to make runtimes
// simpler in the future.
// OPTIM: We shouldn't have to pass static values for every request here.
func (e ContainerRunPayload) MarshalJSON() ([]byte, error) {
	var parameters any = e.Parameters
	if e.Parameters == nil {
		// Default to an empty object instead of null.
		parameters = json.RawMessage([]byte("{}"))
	}

	env := map[string]any{
		"value":          parameters,
		"activation_id":  e.Environment.ActivationID,
		"transaction_id": e.Environment.TransactionID,
		"namespace":      e.Environment.Namespace,
		"action_name":    e.Environment.FunctionName,
		"action_version": e.Environment.FunctionVersion,
		"deadline":       fmt.Sprint(e.Environment.Deadline.UnixMilli()), // Needs to be a string!
	}
	if e.Environment.APIKey != "" {
		env["api_key"] = e.Environment.APIKey
	}

	return json.Marshal(env)
}

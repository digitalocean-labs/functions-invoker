package protocol

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/valyala/fastjson"

	"invoker/pkg/encryption"
)

// ActivationMessage is a message sent from the controller to trigger the invocation of a function.
type ActivationMessage struct {
	// TransactionId is the ID of the current transaction.
	TransactionID string
	// TransactionStart is when the TransactionId started.
	TransactionStart time.Time
	// ActivationID is the ID of the current activation.
	ActivationID string
	// RootController is the index of the controller that sent the message.
	RootControllerIndex string
	// CausedBy is an optional ActivationID pointing at the activation that caused this one.
	CausedBy string
	// IsBlocking defines whether or not the invocation came from a sync call.
	IsBlocking bool

	// Function specifies details about which function should be executed.
	Function ActivationMessageFunction
	// InvocationIdentity specifies which subject and namespace executed the function.
	InvocationIdentity InvocationIdentity

	// Parameters are the parameters passed to the function via the invocation.
	Parameters map[string]json.RawMessage
	// InitParameters are the parameters passed to the function via environment.
	InitParameters map[string]json.RawMessage
}

// ActivationMessageFunction specifies details about which function should be executed as part of
// an ActivationMessage.
type ActivationMessageFunction struct {
	// Namespace is the namespace of the function.
	Namespace string
	// Name is the name of the function.
	Name string
	// Version is the version of the function.
	Version string
	// Revision is the revision of the function (CouchDB specific).
	Revision string
	// Binding is the name of the binding to a package that this function is part of.
	Binding string
}

// InvocationIdentity specifies which subject and namespace executed a function.
type InvocationIdentity struct {
	// Subject is the user that invoked the function.
	Subject string
	// Namespace is the namespace the function was invoked with.
	Namespace string
	// Annotations are annotations on the namespace.
	Annotations map[string]string
	// APIKey was the key used to invoke the function.
	APIKey string
	// StoreActivations defines whether or not activations should be written for this user.
	StoreActivations bool
}

// ParseActivationMessage parses an activation message. This also resolves locked parameters and
// partitions init and "run" parameters.
//
// OPTIM: This allocates quite a lot. At some point, we might consider reusing the incoming buffer
// better instead of doing copys for essentially all values here.
func ParseActivationMessage(data []byte, decryptor *encryption.Decryptor) (*ActivationMessage, error) {
	parsed, err := fastjson.ParseBytes(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	tidVals := parsed.GetArray("transid")
	if len(tidVals) != 2 {
		return nil, errors.New(`malformed input: "transid" was not an array of length 2`)
	}

	actionVal := parsed.Get("action")
	userVals := parsed.Get("user")
	nsVals := userVals.Get("namespace")

	var nsAnnotations map[string]string
	if parsedNsAnnotations := nsVals.GetArray("annotations"); len(parsedNsAnnotations) > 0 {
		nsAnnotations = make(map[string]string, len(parsedNsAnnotations))
		for _, anno := range parsedNsAnnotations {
			nsAnnotations[string(anno.GetStringBytes("key"))] = string(anno.GetStringBytes("value"))
		}
	}

	// Build a set to be able to efficiently look up which args are init vals.
	var initParameterKeys map[string]struct{}
	if initParameterKeyVals := parsed.GetArray("initArgs"); len(initParameterKeyVals) > 0 {
		initParameterKeys = make(map[string]struct{}, len(initParameterKeyVals))
		for _, keyVal := range initParameterKeyVals {
			initParameterKeys[string(keyVal.GetStringBytes())] = struct{}{}
		}
	}
	lockedParameters := parsed.GetObject("lockedArgs")

	// ALL parameters are passed down via this message today. We do not use the parameters we're
	// getting by fetching the function for instance. The controller is passing additional
	// information to be able to tell init and locked parameters apart without the metadata from
	// the function (or the package). We're deconstructing all that below to keep the general
	// business logic clean from it.
	//
	// OPTIM: This is quite a big waste of bytes on the network, especially given that functions
	// are mostly cached. It's even more of a waste considering we're passing along encrypted
	// values in base64 encoded which hugely bloats even small parameters.
	var initParameters map[string]json.RawMessage
	var runParameters map[string]json.RawMessage

	if content := parsed.GetObject("content"); content != nil {
		parameters := make(map[string]*fastjson.Value, content.Len())
		content.Visit(func(keyBs []byte, v *fastjson.Value) {
			parameters[string(keyBs)] = v
		})

		initParameters = make(map[string]json.RawMessage, len(initParameterKeys))
		runParameters = make(map[string]json.RawMessage, len(parameters)-len(initParameterKeys))

		for k, v := range parameters {
			var value json.RawMessage

			if encryptionKeyVal := lockedParameters.Get(k); encryptionKeyVal != nil {
				// If this parameter is encrypted, it's sent as a base64-encoded representation
				// and thus first needs to be base64-decoded.
				encryptionKey := string(encryptionKeyVal.GetStringBytes())
				base64Encoded := v.GetStringBytes()
				decoded := make([]byte, base64.StdEncoding.DecodedLen(len(base64Encoded)))
				n, err := base64.StdEncoding.Decode(decoded, base64Encoded)
				if err != nil {
					return nil, fmt.Errorf("failed to base64-decode encrypted value: %w", err)
				}
				unlocked, err := decryptor.Decrypt(encryptionKey, decoded[:n])
				if err != nil {
					return nil, fmt.Errorf("failed to decrypt value: %w", err)
				}
				value = unlocked
			} else {
				value = json.RawMessage(v.MarshalTo([]byte{}))
			}

			if _, ok := initParameterKeys[k]; ok {
				initParameters[k] = value
			} else {
				runParameters[k] = value
			}
		}
	}

	// Default storeActivations to true and only override it if it's explicitly defined.
	storeActivations := true
	if storeActivationVal := userVals.Get("limits", "storeActivations"); storeActivationVal != nil {
		storeActivations = storeActivationVal.GetBool()
	}

	return &ActivationMessage{
		TransactionID:       string(tidVals[0].GetStringBytes()),
		TransactionStart:    time.UnixMilli(tidVals[1].GetInt64()),
		ActivationID:        string(parsed.GetStringBytes("activationId")),
		RootControllerIndex: string(parsed.GetStringBytes("rootControllerIndex", "asString")),
		CausedBy:            string(parsed.GetStringBytes("cause")),
		IsBlocking:          parsed.GetBool("blocking"),
		Function: ActivationMessageFunction{
			Namespace: string(actionVal.GetStringBytes("path")),
			Name:      string(actionVal.GetStringBytes("name")),
			Version:   string(actionVal.GetStringBytes("version")),
			Binding:   string(actionVal.GetStringBytes("binding")),
			Revision:  string(parsed.GetStringBytes("revision")),
		},
		InvocationIdentity: InvocationIdentity{
			Subject:          string(userVals.GetStringBytes("subject")),
			Namespace:        string(nsVals.GetStringBytes("name")),
			Annotations:      nsAnnotations,
			APIKey:           string(userVals.GetStringBytes("authkey", "api_key")),
			StoreActivations: storeActivations,
		},
		Parameters:     runParameters,
		InitParameters: initParameters,
	}, nil
}

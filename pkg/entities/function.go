package entities

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/valyala/fastjson"

	"invoker/pkg/units"
)

//go:generate mockgen -package mocks -source=function.go -destination mocks/function_mock.go

var (
	// ErrFunctionNotFound is returned if a given function can not be found.
	ErrFunctionNotFound = errors.New("function not found")
)

// FunctionReader can get functions.
type FunctionReader interface {
	// GetFunction gets the given function.
	GetFunction(ctx context.Context, fqdn string, rev string) (*Function, error)
}

// Function is a representation of a function as created in the system.
type Function struct {
	// Name is the name of the function.
	Name string `json:"name"`
	// Namespace is the namespace of the function.
	Namespace string `json:"namespace"`
	// Exec contains information about the function's code.
	Exec Exec `json:"exec"`
	// Annotations are the key-value pairs to annotate the function with.
	Annotations Annotations `json:"annotations"`
	// Limits define the limits the function works in.
	Limits Limits `json:"limits"`
}

// Exec defines the details of the function's code.
type Exec struct {
	// Kind is the runtime the function runs in.
	Kind string `json:"kind"`
	// Code is the function's actual code.
	Code FunctionCode `json:"code"`
	// Main defines the entrypoint for the function.
	Main string `json:"main"`
	// Binary defines whether or not Code is base64-encoded binary data.
	Binary bool `json:"binary"`
}

// Limits define the limits the function works in.
type Limits struct {
	// Timeout is the amount of time the function can run.
	Timeout time.Duration
	// Memory is the amount of memory a function can use.
	Memory units.ByteSize
	// Logs is the amount of logs a function can write.
	Logs units.ByteSize
}

// MarshalJSON implements the Golang stdlib marshaller.
//
// We need this as limits are written into the Activation as well and it's serialization must match
// what the controller expects.
func (l Limits) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf(`{"timeout":%d,"memory":%d,"logs":%d}`,
		l.Timeout.Milliseconds(),
		l.Memory/units.Megabyte,
		l.Logs/units.Kilobyte)), nil
}

// UnmarshallJSON implements the Golang stdlib unmarshaller.
func (l *Limits) UnmarshalJSON(data []byte) error {
	parsed, err := fastjson.ParseBytes(data)
	if err != nil {
		return err
	}

	*l = Limits{
		Timeout: time.Duration(parsed.GetInt64("timeout")) * time.Millisecond,
		Memory:  units.ByteSize(parsed.GetInt64("memory")) * units.Megabyte,
		Logs:    units.ByteSize(parsed.GetInt64("logs")) * units.Kilobyte,
	}
	return nil
}

// FunctionCode contains the function's code. It deals with different ways of inlining or attaching
// code.
type FunctionCode struct {
	// Resolved contains the "resolved" code. This can be forwarded to the container as-is as it's
	// either plain text code or a base64 encoded string.
	Resolved string

	// AttachmentID is the ID of the attachment.
	AttachmentID string
	// IsBinary defines whether or not the attachment is binary data.
	IsBinary bool
}

var (
	// memPrefix specifies the "protocol" of an attachment that inlines the code into a "URL".
	memPrefix = []byte("mem:")
	// couchPrefix specifies the "protocol" of an attachment that is an actual CouchDB attachment.
	couchPrefix = []byte("couch:")

	// binaryType specifies the content-type used if an attachment represents binary data.
	binaryType = []byte("application/octet-stream")
)

// UnmarshalJSON implements the Golang stdlib unmarshaller.
//
// This code deals with 5 different cases:
//  1. Inlined plain text code.
//  2. Inlined attachments (with mem: prefix) ...
//     2.1 ... with plain text code.
//     2.2 ... with binary code.
//  3. External attachments (with couch: prefix) ...
//     3.1. ... with plain text code.
//     3.2. ... with binary code.
//
// For performance reasons, the controller inlines any functions that are smaller than 16k into
// the document rather than creating an additional attachment. That safes an extra HTTP request
// for these small functions. For an "inlined attachment", the respective function is base64
// URL-encoded into the attachmentName with the "mem:" prefix.
//
// OPTIM: Challenge this "optimization" if we're redoing our function code storage. I doubt that
// it really matters considering that most of the function access is supposed to be cached anyway.
func (c *FunctionCode) UnmarshalJSON(data []byte) error {
	parsed, err := fastjson.ParseBytes(data)
	if err != nil {
		return fmt.Errorf("failed to parse code: %w", err)
	}

	// If we're looking at a string, we assume that it's inlined code.
	if parsed.Type() == fastjson.TypeString {
		c.Resolved = string(parsed.GetStringBytes())
		return nil
	}

	// Otherwise, we assume it's an object and thus contains attachment information.
	name := parsed.GetStringBytes("attachmentName")
	isBinary := bytes.Equal(parsed.GetStringBytes("attachmentType"), binaryType)

	// If the attachment name has a prefix "mem:" it's an inlined attachment.
	if bytes.HasPrefix(name, memPrefix) {
		length := parsed.GetInt("length")

		// If the length of the inlined attachment is 0, the function was created with an empty file.
		if length == 0 {
			c.Resolved = ""
			return nil
		}

		inlined := bytes.TrimPrefix(name, memPrefix)

		// The controller encodes inlined attachments as a fake URL with a base64 encoded version
		// of the actual code. Since all subsequent systems use standard encoding, we decode first.
		decoded := make([]byte, length)
		n, err := base64.URLEncoding.Decode(decoded, inlined)
		if err != nil {
			return fmt.Errorf("failed to base64 decode inlined attachment: %w", err)
		}

		// If the value is an actual binary (like a small, inlined zip for example), reencode using
		// standard encoding so subsequent systems can read this in all cases.
		if isBinary {
			c.Resolved = base64.StdEncoding.EncodeToString(decoded[:n])
			return nil
		}

		// Otherwise we're dealing with plaintext and can just use that value.
		c.Resolved = string(decoded)
		return nil
	}

	// Otherwise, it's a "remote" attachment, that we'll have to resolve later.
	id := bytes.TrimPrefix(name, couchPrefix)
	c.AttachmentID = string(id)
	c.IsBinary = isBinary

	return nil
}

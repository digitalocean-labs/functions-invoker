package couchdb

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"invoker/pkg/entities"
	"invoker/pkg/units"
)

var (
	// This happens for very small functions, think a quick single-file Node.js function.
	//go:embed testdata/function_inlined_attachment_plain.json
	functionInlinedAttachmentPlain []byte

	// This happens for functions created with empty files.
	//go:embed testdata/function_inlined_attachment_empty.json
	functionInlinedAttachmentEmpty []byte

	// This happens for very large plaintext files. A Node.js function built with webpack might end
	// up in this case.
	//go:embed testdata/function_external_attachment_plain.json
	functionExternalAttachmentPlain []byte

	// This happens for slightly larger zips and binaries.
	//go:embed testdata/function_external_attachment_binary.json
	functionExternalAttachmentBinary []byte

	// This is a response as returned from the blockedNamespaces view.
	//go:embed testdata/blocked_namespaces.json
	blockedNamespacesResponse []byte
)

func TestGetFunction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		db                *fakeCouchDB
		fqdn              string
		rev               string
		want              *entities.Function
		wantError         bool
		wantSpecificError error
	}{{
		name: "function with inlined attachment in plaintext",
		db: &fakeCouchDB{
			functions: map[string][]byte{
				"ns/pkg/functionInlined@rev1": functionInlinedAttachmentPlain,
			},
		},
		fqdn: "ns/pkg/functionInlined",
		rev:  "rev1",
		want: &entities.Function{
			Name:      "test",
			Namespace: "testuser",
			Annotations: entities.Annotations{
				"provide-api-key": false,
				"exec":            "nodejs:14",
			},
			Limits: entities.Limits{
				Timeout: 3 * time.Second,
				Memory:  256 * units.Megabyte,
				Logs:    16 * units.Kilobyte,
			},
			Exec: entities.Exec{
				Kind: "nodejs:14",
				Code: entities.FunctionCode{
					Resolved: `function main() { return {} }`,
				},
			},
		},
	}, {
		name: "function with an empty inlined attachment",
		db: &fakeCouchDB{
			functions: map[string][]byte{
				"ns/pkg/functionInlined@rev1": functionInlinedAttachmentEmpty,
			},
		},
		fqdn: "ns/pkg/functionInlined",
		rev:  "rev1",
		want: &entities.Function{
			Name:      "test",
			Namespace: "testuser",
			Annotations: entities.Annotations{
				"provide-api-key": false,
				"exec":            "nodejs:14",
			},
			Limits: entities.Limits{
				Timeout: 3 * time.Second,
				Memory:  256 * units.Megabyte,
				Logs:    16 * units.Kilobyte,
			},
			Exec: entities.Exec{
				Kind: "nodejs:14",
				Code: entities.FunctionCode{
					Resolved: "",
				},
			},
		},
	}, {
		name: "function with external attachment in plain",
		db: &fakeCouchDB{
			functions: map[string][]byte{
				"ns/pkg/functionExternalPlain@rev3":                                      functionExternalAttachmentPlain,
				"ns/pkg/functionExternalPlain/a184456c-a0b8-4291-8445-6ca0b822917f@rev3": []byte("testattachment"),
			},
		},
		fqdn: "ns/pkg/functionExternalPlain",
		rev:  "rev3",
		want: &entities.Function{
			Name:      "test",
			Namespace: "testuser",
			Annotations: entities.Annotations{
				"provide-api-key": false,
				"exec":            "nodejs:14",
			},
			Limits: entities.Limits{
				Timeout: 3 * time.Second,
				Memory:  256 * units.Megabyte,
				Logs:    16 * units.Kilobyte,
			},
			Exec: entities.Exec{
				Kind: "nodejs:14",
				Code: entities.FunctionCode{
					Resolved:     "testattachment", // Attachment got fetched and resolved to plaintext.
					AttachmentID: "a184456c-a0b8-4291-8445-6ca0b822917f",
					IsBinary:     false,
				},
				Binary: false,
			},
		},
	}, {
		name: "function with external attachment in binary",
		db: &fakeCouchDB{
			functions: map[string][]byte{
				"ns/pkg/functionExternalBinary@rev2":                                      functionExternalAttachmentBinary,
				"ns/pkg/functionExternalBinary/50ddbba2-b12b-4a62-9dbb-a2b12bba62c1@rev2": []byte("testattachment"),
			},
		},
		fqdn: "ns/pkg/functionExternalBinary",
		rev:  "rev2",
		want: &entities.Function{
			Name:      "test",
			Namespace: "testuser",
			Annotations: entities.Annotations{
				"provide-api-key": false,
				"exec":            "nodejs:14",
			},
			Limits: entities.Limits{
				Timeout: 3 * time.Second,
				Memory:  256 * units.Megabyte,
				Logs:    16 * units.Kilobyte,
			},
			Exec: entities.Exec{
				Kind: "nodejs:14",
				Code: entities.FunctionCode{
					Resolved:     "dGVzdGF0dGFjaG1lbnQ=", // Attachment got fetched and resolved to base64.
					AttachmentID: "50ddbba2-b12b-4a62-9dbb-a2b12bba62c1",
					IsBinary:     true,
				},
				Binary: true,
			},
		},
	}, {
		name:              "function not found",
		db:                &fakeCouchDB{},
		fqdn:              "ns/pkg/functionNotFound",
		rev:               "rev2",
		wantError:         true,
		wantSpecificError: entities.ErrFunctionNotFound,
	}, {
		name: "function revision not found",
		db: &fakeCouchDB{
			functions: map[string][]byte{
				"ns/pkg/functionInlined@rev1": functionInlinedAttachmentPlain,
			},
		},
		fqdn:              "ns/pkg/functionInlined",
		rev:               "rev2",
		wantError:         true,
		wantSpecificError: entities.ErrFunctionNotFound,
	}, {
		name: "function attachment not found",
		db: &fakeCouchDB{
			functions: map[string][]byte{
				"ns/pkg/functionExternalBinary@rev2":                    functionExternalAttachmentBinary,
				"ns/pkg/functionExternalBinary/not-the-attachment@rev2": []byte("testattachment"),
			},
		},
		fqdn:              "ns/pkg/functionExternalBinary",
		rev:               "rev2",
		wantError:         true,
		wantSpecificError: entities.ErrFunctionNotFound,
	}, {
		name: "function malformed",
		db: &fakeCouchDB{
			functions: map[string][]byte{
				"ns/pkg/functionMalformed@rev1": []byte(`[]`),
			},
		},
		fqdn:      "ns/pkg/functionMalformed",
		rev:       "rev1",
		wantError: true,
	}, {
		name:              "database not reachable",
		db:                &fakeCouchDB{err: assert.AnError},
		fqdn:              "ns/pkg/functionMalformed",
		rev:               "rev1",
		wantError:         true,
		wantSpecificError: assert.AnError,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Keeps the retries short but still happening a few times.
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()

			client := &Client{
				ClientFactory: func() *http.Client { return &http.Client{Transport: tt.db} },
				Base:          &url.URL{},
				Username:      "foo",
				Password:      "bar",
			}
			got, err := client.GetFunction(ctx, tt.fqdn, tt.rev)
			if tt.wantError {
				require.Error(t, err)
				if tt.wantSpecificError != nil {
					require.ErrorIs(t, err, tt.wantSpecificError)
				}
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.want, got)
		})
	}
}

func TestWriteDocuments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		db                *fakeCouchDB
		docs              []json.RawMessage
		want              map[string][]byte
		wantError         bool
		wantSpecificError error
	}{{
		name: "insert a single document",
		db:   &fakeCouchDB{activations: make(map[string][]byte), insertReturnCode: http.StatusCreated},
		docs: []json.RawMessage{[]byte(`{"_id":"test","foo":"bar"}`)},
		want: map[string][]byte{"test": []byte(`{"_id":"test","foo":"bar"}`)},
	}, {
		name: "insert a single document getting an accepted response",
		db:   &fakeCouchDB{activations: make(map[string][]byte), insertReturnCode: http.StatusAccepted},
		docs: []json.RawMessage{[]byte(`{"_id":"test","foo":"bar"}`)},
		want: map[string][]byte{"test": []byte(`{"_id":"test","foo":"bar"}`)},
	}, {
		name: "insert multiple documents",
		db:   &fakeCouchDB{activations: make(map[string][]byte), insertReturnCode: http.StatusCreated},
		docs: []json.RawMessage{
			[]byte(`{"_id":"test","foo":"bar"}`),
			[]byte(`{"_id":"test2","foo":"bar2"}`),
			[]byte(`{"_id":"test3","foo":"bar3"}`),
		},
		want: map[string][]byte{
			"test":  []byte(`{"_id":"test","foo":"bar"}`),
			"test2": []byte(`{"_id":"test2","foo":"bar2"}`),
			"test3": []byte(`{"_id":"test3","foo":"bar3"}`),
		},
	}, {
		name:      "database doesn't exist",
		db:        &fakeCouchDB{},
		docs:      []json.RawMessage{[]byte(`{"_id":"test","foo":"bar"}`)},
		wantError: true,
	}, {
		name:              "database not reachable",
		db:                &fakeCouchDB{err: assert.AnError},
		docs:              []json.RawMessage{[]byte(`{"_id":"test","foo":"bar"}`)},
		wantError:         true,
		wantSpecificError: assert.AnError,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Keeps the retries short but still happening a few times.
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()

			client := &Client{
				ClientFactory: func() *http.Client { return &http.Client{Transport: tt.db} },
				Base:          &url.URL{},
				Username:      "foo",
				Password:      "bar",
			}
			err := client.WriteDocuments(ctx, activationsDB, tt.docs)
			if tt.wantError {
				require.Error(t, err)
				if tt.wantSpecificError != nil {
					require.ErrorIs(t, err, tt.wantSpecificError)
				}
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.want, tt.db.activations)
		})
	}
}

func TestGetBlockedNamespaces(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		db                http.RoundTripper
		want              map[string]struct{}
		wantError         bool
		wantSpecificError error
	}{{
		name: "full example",
		db: fakeTransportFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(blockedNamespacesResponse)),
			}, nil
		}),
		want: map[string]struct{}{
			"denylisttest":  {},
			"denylisttest2": {},
			"denylisttest3": {},
		},
	}, {
		name: "empty response",
		db: fakeTransportFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"total_rows":0,"offset":0,"rows":[]}`)),
			}, nil
		}),
		want: nil,
	}, {
		name: "database not reachable",
		db: fakeTransportFunc(func(r *http.Request) (*http.Response, error) {
			return nil, assert.AnError
		}),
		wantError:         true,
		wantSpecificError: assert.AnError,
	}, {
		name: "non 200 response",
		db: fakeTransportFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusInternalServerError}, nil
		}),
		wantError: true,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Keeps the retries short but still happening a few times.
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()

			client := &Client{
				ClientFactory: func() *http.Client { return &http.Client{Transport: tt.db} },
				Base:          &url.URL{},
				Username:      "foo",
				Password:      "bar",
			}
			got, err := client.GetBlockedNamespaces(ctx)
			if tt.wantError {
				require.Error(t, err)
				if tt.wantSpecificError != nil {
					require.ErrorIs(t, err, tt.wantSpecificError)
				}
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.want, got)
		})
	}
}

type fakeCouchDB struct {
	functions   map[string][]byte
	activations map[string][]byte

	insertReturnCode int
	err              error
}

func (c *fakeCouchDB) RoundTrip(req *http.Request) (*http.Response, error) {
	if c.err != nil {
		return nil, c.err
	}

	pathSegments := strings.SplitN(req.URL.Path, "/", 2)
	if len(pathSegments) < 2 {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Body:       io.NopCloser(strings.NewReader("malformed path, expected at least 2 parts")),
		}, nil
	}

	var db map[string][]byte
	dbName := pathSegments[0]
	switch dbName {
	case functionsDB:
		db = c.functions
	case activationsDB:
		db = c.activations
	}
	if db == nil {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(strings.NewReader(fmt.Sprintf("database %q not found", dbName))),
		}, nil
	}

	docID := pathSegments[1]
	if req.Method == http.MethodGet {
		if rev := req.URL.Query().Get("rev"); rev != "" {
			docID += "@" + rev
		}
		return c.get(db, docID), nil
	} else if req.Method == http.MethodPost && docID == "_bulk_docs" {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read request body")
		}
		return c.bulkInsert(db, body)
	}

	return &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader("malformed request")),
	}, nil
}

func (c *fakeCouchDB) get(db map[string][]byte, docID string) *http.Response {
	doc, ok := db[docID]
	if !ok {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(strings.NewReader(fmt.Sprintf("doc %q not found", docID))),
		}
	}

	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(doc)),
	}
}

func (c *fakeCouchDB) bulkInsert(db map[string][]byte, body []byte) (*http.Response, error) {
	var req bulkInsertRequest
	err := json.Unmarshal(body, &req)
	if err != nil {
		return nil, fmt.Errorf("failed to parse bulk insert body: %w", err)
	}

	for _, doc := range req.Docs {
		var parsedDoc struct {
			ID string `json:"_id"`
		}
		err := json.Unmarshal(doc, &parsedDoc)
		if err != nil {
			return nil, fmt.Errorf("failed to parse individual document in insert body: %w", err)
		}
		db[parsedDoc.ID] = doc
	}

	return &http.Response{
		StatusCode: c.insertReturnCode,
	}, nil
}

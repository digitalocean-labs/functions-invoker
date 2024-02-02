package couchdb

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"invoker/pkg/entities"
)

var (
	_ entities.FunctionReader = (*Client)(nil)

	errNotFound = errors.New("document not found")
)

const (
	// functionsDB is the database that contains functions.
	functionsDB = "whisks"
	// activationsDB is the database that contains activations.
	activationsDB = "activations"

	// Settings for exponentially backing off database operations.
	// These result in the following intervals (in ms): 50, 100, 200, 400, 800, 1600, 3200, 5000, 5000, 5000...
	baseRetryInterval   = 50 * time.Millisecond
	retryIntervalFactor = 2
	maxRetryInterval    = 5 * time.Second
	maxRetryDuration    = 2 * time.Minute // Retry for 2 minutes to be able to paper over a K8s upgrade.

	// maxConnectionLifetime controls how long a connection is allowed to exist overall.
	maxConnectionLifetime = 15 * time.Second
)

// Client is a CouchDB specific wrapper for *http.Client.
type Client struct {
	ClientFactory func() *http.Client
	Base          *url.URL
	Username      string
	Password      string

	mux             sync.RWMutex
	client          *http.Client
	clientCreatedAt time.Time
}

// GetFunction implements entities.FunctionReader.
func (c *Client) GetFunction(ctx context.Context, fqdn string, rev string) (*entities.Function, error) {
	getDocStart := time.Now()
	metricGetDocument.Inc()
	bs, err := c.getRequest(ctx, c.Base.JoinPath(functionsDB, url.PathEscape(fqdn)), rev)
	if err != nil {
		metricGetDocumentFailures.Inc()
		if errors.Is(err, errNotFound) {
			// Transform into a generic function not found error.
			err = entities.ErrFunctionNotFound
		}
		return nil, fmt.Errorf("failed to fetch function: %w", err)
	}
	metricGetDocumentLatency.Observe(time.Since(getDocStart).Seconds())

	var fn entities.Function
	if err := json.Unmarshal(bs, &fn); err != nil {
		return nil, fmt.Errorf("failed to parse function: %w", err)
	}

	// If there's no attachment ID, the code was already resolved and we're good.
	if fn.Exec.Code.AttachmentID == "" {
		return &fn, nil
	}

	// Otherwise, we have to also fetch the attachment.
	getAttachmentStart := time.Now()
	metricGetAttachment.Inc()
	bs, err = c.getRequest(ctx, c.Base.JoinPath(functionsDB, url.QueryEscape(fqdn), fn.Exec.Code.AttachmentID), rev)
	if err != nil {
		metricGetAttachmentFailures.Inc()
		if errors.Is(err, errNotFound) {
			// Transform into a generic function not found error.
			err = entities.ErrFunctionNotFound
		}
		return nil, fmt.Errorf("failed to fetch attachment: %w", err)
	}
	metricGetAttachmentLatency.Observe(time.Since(getAttachmentStart).Seconds())

	// If the code isn't binary, we can pass it along as is.
	if !fn.Exec.Code.IsBinary {
		fn.Exec.Code.Resolved = string(bs)
		return &fn, nil
	}

	// Otherwise we have to base64 encode it before we're able to pass it down.
	fn.Exec.Code.Resolved = base64.StdEncoding.EncodeToString(bs)
	return &fn, nil
}

// bulkInsertRequest is the schema for creating a bulk insertion.
type bulkInsertRequest struct {
	Docs []json.RawMessage `json:"docs"`
}

// WriteDocuments writes the given documents to the given db in bulk.
func (c *Client) WriteDocuments(ctx context.Context, db string, docs []json.RawMessage) error {
	metricBatchSize.Observe(float64(len(docs)))
	bs, err := json.Marshal(bulkInsertRequest{Docs: docs})
	if err != nil {
		return fmt.Errorf("failed to marshal bulk request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, maxRetryDuration)
	defer cancel()

	start := time.Now()
	metricSaveDocumentBulk.Inc()
	resp, err := c.request(ctx, http.MethodPost, c.Base.JoinPath(db, "_bulk_docs"), "", bs)
	if err != nil {
		metricSaveDocumentBulkFailures.Inc()
		return fmt.Errorf("failed to execute bulk activation request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
		metricSaveDocumentBulkFailures.Inc()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read body from bulk activation request: %w", err)
		}
		return fmt.Errorf("unexpected status code for bulk activation: %d, body %q", resp.StatusCode, body)
	}

	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		metricSaveDocumentBulkFailures.Inc()
		return fmt.Errorf("failed to read body from bulk activation: %w", err)
	}
	metricSaveDocumentBulkLatency.Observe(time.Since(start).Seconds())

	return nil
}

// blockedNamespacesViewResponse is the schema of a response of the blockedNamespaces view.
type blockedNamespacesViewResponse struct {
	Rows []struct {
		Key string `json:"key"`
	} `json:"rows"`
}

// GetBlockedNamespaces gets all blocked namespaces as a "set".
func (c *Client) GetBlockedNamespaces(ctx context.Context) (map[string]struct{}, error) {
	url := c.Base.JoinPath("subjects", "_design", "namespaceThrottlings", "_view", "blockedNamespaces")
	query := url.Query()
	query.Add("reduce", "false")
	url.RawQuery = query.Encode()

	resp, err := c.getRequest(ctx, url, "")
	if err != nil {
		return nil, fmt.Errorf("failed to execute request to get blocked namespaces: %w", err)
	}

	var viewResponse blockedNamespacesViewResponse
	err = json.Unmarshal(resp, &viewResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to parse blocked namespaces request: %w", err)
	}

	if len(viewResponse.Rows) == 0 {
		//nolint:nilnil // A nil map can transparently be read from, so that's fine here.
		return nil, nil
	}

	blockedNamespaces := make(map[string]struct{}, len(viewResponse.Rows))
	for _, row := range viewResponse.Rows {
		blockedNamespaces[row.Key] = struct{}{}
	}
	return blockedNamespaces, nil
}

// getDocument executes a GET request and returns the bytes of the response.
func (c *Client) getRequest(ctx context.Context, url *url.URL, rev string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, maxRetryDuration)
	defer cancel()

	resp, err := c.request(ctx, http.MethodGet, url, rev, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to execute get request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read body from fetched document(s): %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code for fetching document(s): %d, body %q", resp.StatusCode, body)
	}

	return body, nil
}

// request executes a request and returns the entire response. Make sure to consume and close it!
func (c *Client) request(ctx context.Context, method string, url *url.URL, rev string, content []byte) (*http.Response, error) {
	if rev != "" {
		query := url.Query()
		query.Add("rev", rev)
		url.RawQuery = query.Encode()
	}

	retryInterval := baseRetryInterval
	var lastErr error
	for retries := 0; ; retries++ {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("failed to successfully reach database after %d retries: %w", retries, lastErr)
		}

		resp, err := c.singleRequest(ctx, method, url.String(), content)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				// Skip storing the error and sleeping to not mask the underlying error and finish
				// early.
				continue
			}

			lastErr = err
			retryInterval *= retryIntervalFactor
			if retryInterval > maxRetryInterval {
				retryInterval = maxRetryInterval
			}
			time.Sleep(retryInterval)
			continue
		}
		return resp, nil
	}
}

// singleRequest executes a single request and returns the entire response. Make sure to consume
// and close it.
func (c *Client) singleRequest(ctx context.Context, method string, url string, content []byte) (*http.Response, error) {
	var body io.Reader
	if content != nil {
		body = bytes.NewReader(content)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.SetBasicAuth(c.Username, c.Password)
	req.Header.Add("Content-Type", "application/json")
	resp, err := c.getClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}

	return resp, nil
}

// getClient returns an HTTP client, potentially creating a new one if the current one has exceed
// its lifetime.
// We recreate the entire client (which is supposed to create a new transport as well) since Golang
// doesn't allow us to control maximum connection lifetime right now.
func (c *Client) getClient() *http.Client {
	c.mux.RLock()
	if c.client != nil && time.Since(c.clientCreatedAt) < maxConnectionLifetime {
		defer c.mux.RUnlock()
		return c.client
	}
	c.mux.RUnlock()

	c.mux.Lock()
	defer c.mux.Unlock()

	// Doubly checked locking to avoid creating too many clients.
	if c.client != nil && time.Since(c.clientCreatedAt) < maxConnectionLifetime {
		return c.client
	}

	c.client = c.ClientFactory()
	c.clientCreatedAt = time.Now()
	return c.client
}

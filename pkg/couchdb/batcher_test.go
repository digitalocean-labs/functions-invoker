package couchdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"invoker/pkg/entities"
)

func TestActivationBatcher(t *testing.T) {
	t.Parallel()

	a1 := &entities.Activation{
		ActivationID: "testaid1",
		Namespace:    "testnamespace",
	}
	a1bs, err := json.Marshal(a1)
	require.NoError(t, err)
	a1bs = append(a1bs[:len(a1bs)-1], `,"_id":"testnamespace/testaid1"}`...)

	a2 := &entities.Activation{
		ActivationID: "testaid2",
		Namespace:    "testnamespace",
	}
	a2bs, err := json.Marshal(a2)
	require.NoError(t, err)
	a2bs = append(a2bs[:len(a2bs)-1], `,"_id":"testnamespace/testaid2"}`...)

	a3 := &entities.Activation{
		ActivationID: "testaid3",
		Namespace:    "testnamespace",
	}
	a3bs, err := json.Marshal(a3)
	require.NoError(t, err)
	a3bs = append(a3bs[:len(a3bs)-1], `,"_id":"testnamespace/testaid3"}`...)

	// Manually set up the batcher to be able to control the timing.
	batcher := &ActivationBatcher{
		activations: make(chan json.RawMessage),
		doneCh:      make(chan struct{}),
	}
	defer batcher.Shutdown()

	requests := make(chan *http.Request)
	client := &Client{
		ClientFactory: func() *http.Client {
			return &http.Client{Transport: fakeTransportFunc(func(r *http.Request) (*http.Response, error) {
				requests <- r
				return &http.Response{StatusCode: http.StatusCreated}, nil
			})}
		},
		Base: &url.URL{},
	}
	w := &worker{logger: zerolog.New(zerolog.NewTestWriter(t)), client: client}
	batcher.workers = []*worker{w}

	tickerCh := make(chan time.Time)
	go batcher.run(tickerCh)
	go w.run(batcher.activations)

	err = batcher.WriteActivation(context.Background(), a1)
	require.NoError(t, err)
	err = batcher.WriteActivation(context.Background(), a2)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		w.mux.Lock()
		defer w.mux.Unlock()
		return len(w.batch) == 2
	}, 5*time.Second, 1*time.Millisecond)

	// Tick and wait for the request to be sent.
	tickerCh <- time.Now()
	req := <-requests

	// Assert the request contains the first 2 activations.
	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	want := fmt.Sprintf(`{"docs":[%s,%s]}`, a1bs, a2bs)
	require.JSONEq(t, want, string(body))

	// Write another activation.
	err = batcher.WriteActivation(context.Background(), a3)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		w.mux.Lock()
		defer w.mux.Unlock()
		return len(w.batch) == 1
	}, 5*time.Second, 1*time.Millisecond)

	// Tick and wait for the request to be sent.
	tickerCh <- time.Now()
	req = <-requests

	// Assert the request contains the 3rd activation.
	body, err = io.ReadAll(req.Body)
	require.NoError(t, err)
	want = fmt.Sprintf(`{"docs":[%s]}`, a3bs)
	require.JSONEq(t, want, string(body))
}

func TestActivationBatcherShutdown(t *testing.T) {
	t.Parallel()

	a1 := &entities.Activation{
		ActivationID: "testaid1",
		Namespace:    "testnamespace",
	}
	a1bs, err := json.Marshal(a1)
	require.NoError(t, err)
	a1bs = append(a1bs[:len(a1bs)-1], `,"_id":"testnamespace/testaid1"}`...)

	requests := make(chan *http.Request, 1)
	client := &Client{
		ClientFactory: func() *http.Client {
			return &http.Client{Transport: fakeTransportFunc(func(r *http.Request) (*http.Response, error) {
				requests <- r
				return &http.Response{StatusCode: http.StatusCreated}, nil
			})}
		},
		Base: &url.URL{},
	}
	batcher := NewActivationBatcher(zerolog.New(zerolog.NewTestWriter(t)), client, 1)

	err = batcher.WriteActivation(context.Background(), a1)
	require.NoError(t, err)

	// Shut the batcher down. This should trigger an immediate flush.
	batcher.Shutdown()

	// Wait for the request to be flushed.
	req := <-requests

	// Assert the request contains the activation.
	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	want := fmt.Sprintf(`{"docs":[%s]}`, a1bs)
	require.JSONEq(t, want, string(body))
}

func TestActivationBatcherExceedingBatchSize(t *testing.T) {
	t.Parallel()

	a1 := &entities.Activation{
		ActivationID: "testaid1",
		Namespace:    "testnamespace",
	}
	a1bs, err := json.Marshal(a1)
	require.NoError(t, err)
	a1bs = append(a1bs[:len(a1bs)-1], `,"_id":"testnamespace/testaid1"}`...)

	// The second activation is REALLY big (bigger than the maximum batch size).
	longNamespace := strings.Repeat("A", int(flushBytes))
	a2 := &entities.Activation{
		ActivationID: "testaid2",
		Namespace:    longNamespace,
	}
	a2bs, err := json.Marshal(a2)
	require.NoError(t, err)
	a2bs = append(a2bs[:len(a2bs)-1], fmt.Sprintf(`,"_id":"%s/testaid2"}`, longNamespace)...)

	requests := make(chan *http.Request, 1)
	client := &Client{
		ClientFactory: func() *http.Client {
			return &http.Client{Transport: fakeTransportFunc(func(r *http.Request) (*http.Response, error) {
				requests <- r
				return &http.Response{StatusCode: http.StatusCreated}, nil
			})}
		},
		Base: &url.URL{},
	}
	batcher := NewActivationBatcher(zerolog.New(zerolog.NewTestWriter(t)), client, 1)

	err = batcher.WriteActivation(context.Background(), a1)
	require.NoError(t, err)

	err = batcher.WriteActivation(context.Background(), a2)
	require.NoError(t, err)

	// Wait for the request to be flushed.
	req := <-requests

	// Assert the request contains the first activation.
	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	want := fmt.Sprintf(`{"docs":[%s]}`, a1bs)
	require.JSONEq(t, want, string(body))

	// Shut the batcher down. This should trigger an immediate flush of the second activation.
	batcher.Shutdown()

	// Wait for the request to be flushed.
	req = <-requests

	// Assert the request contains the second activation.
	body, err = io.ReadAll(req.Body)
	require.NoError(t, err)
	want = fmt.Sprintf(`{"docs":[%s]}`, a2bs)
	require.JSONEq(t, want, string(body))
}

// fakeTransportFunc mocks http.RoundTripper in a single function.
type fakeTransportFunc func(*http.Request) (*http.Response, error)

// RoundTrip implements http.RoundTripper.
func (f fakeTransportFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

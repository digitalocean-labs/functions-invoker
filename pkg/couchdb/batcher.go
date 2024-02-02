package couchdb

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"invoker/pkg/entities"
	"invoker/pkg/units"
)

var (
	_ entities.ActivationWriter = (*ActivationBatcher)(nil)
)

const (
	// flushInterval is the interval after which a batch is guaranteed to be flushed.
	flushInterval = 3 * time.Second
	// flushBytes is the maximum size of a batch. If a new activation would cause the batch to exceed
	// this size, it'll get flushed first.
	flushBytes = 5 * units.Megabyte
)

// NewActivationBatcher creates a new batcher with the given client and starts the given amount
// of worker threads writing the activations.
func NewActivationBatcher(logger zerolog.Logger, client *Client, workers int) *ActivationBatcher {
	batcher := &ActivationBatcher{
		activations: make(chan json.RawMessage, workers),
		workers:     make([]*worker, 0, workers),
		doneCh:      make(chan struct{}),
	}

	for i := 0; i < workers; i++ {
		worker := &worker{
			logger: logger.With().Int("worker", i).Logger(),
			client: client,
		}
		batcher.workers = append(batcher.workers, worker)

		batcher.grp.Add(1)
		go func() {
			defer batcher.grp.Done()
			worker.run(batcher.activations)
		}()
	}

	// Launch an extra goroutine to flush all of the workers in the configured interval.
	batcher.grp.Add(1)
	go func() {
		defer batcher.grp.Done()

		ticker := time.NewTicker(flushInterval)
		defer ticker.Stop()
		batcher.run(ticker.C)
	}()

	return batcher
}

// ActivationBatcher writes activations to the backing CouchDB in batches.
type ActivationBatcher struct {
	activations chan json.RawMessage
	workers     []*worker
	doneCh      chan struct{}

	grp sync.WaitGroup
}

// run starts an infinite loop reading activations into batches. Closing the activations channel
// will cause termination after writing a last batch.
func (b *ActivationBatcher) run(tickerCh <-chan time.Time) {
	for {
		select {
		case <-tickerCh:
			// The workers are flushed in sequence. That is: Only one worker will be blocked
			// by the interval-based flushing at a time. The other workers can still consume
			// activations.
			for _, worker := range b.workers {
				worker.mux.Lock()
				worker.flush()
				worker.mux.Unlock()
			}
		case <-b.doneCh:
			return
		}
	}
}

// WriteActivation implements entities.ActivationWriter.
func (b *ActivationBatcher) WriteActivation(_ context.Context, activation *entities.Activation) error {
	// Marshal the activation directly to "free" the pointer and not hold it unnecessarily long.
	bs, err := json.Marshal(activation)
	if err != nil {
		return fmt.Errorf("failed to marshal activation: %w", err)
	}

	// The CouchDB _bulk_docs API requires the _id field to be present, so compute that now.
	// Drop the closing bracket.
	bs = bs[:len(bs)-1]
	// Add the _id field and close the bracket.
	bs = append(bs, fmt.Sprintf(`,"_id":"%s/%s"}`, activation.Namespace, activation.ActivationID)...)

	b.activations <- bs
	return nil
}

// Shutdown gracefully shuts the batcher down.
func (b *ActivationBatcher) Shutdown() {
	close(b.doneCh)
	close(b.activations)
	b.grp.Wait()
}

// worker manages a batch of activations.
type worker struct {
	logger zerolog.Logger

	client *Client

	mux       sync.Mutex
	batch     []json.RawMessage
	batchSize units.ByteSize
}

// run starts an infinite loop collecting activations coming via the passed channel into the batch
// until it exceeds the batch size limit.
// Closing the activations channel will cause termination after flushing the last batch.
func (w *worker) run(activations <-chan json.RawMessage) {
	for activation := range activations {
		w.mux.Lock()
		// If the current activation would exceed the batch maximum, flush first.
		bytesInActivation := units.ByteSize(len(activation))
		if w.batchSize+bytesInActivation > flushBytes {
			w.flush()
		}

		w.batch = append(w.batch, activation)
		w.batchSize += bytesInActivation
		w.mux.Unlock()
	}

	// The channel was closed, so we're shutting down. Write the last batch if present.
	w.mux.Lock()
	w.flush()
	w.mux.Unlock()
}

// flush writes the batch to the database. mux must be held.
func (w *worker) flush() {
	if len(w.batch) == 0 {
		return
	}

	if err := w.client.WriteDocuments(context.Background(), activationsDB, w.batch); err != nil {
		w.logger.Error().Err(err).Msg("failed to write activations")
	}
	w.batch = nil
	w.batchSize = 0
}

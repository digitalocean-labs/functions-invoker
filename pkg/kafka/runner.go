package kafka

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/segmentio/kafka-go"

	"invoker/pkg/entities"
	"invoker/pkg/logging"
	"invoker/pkg/protocol"
)

//go:generate mockgen -package mocks -source=runner.go -destination mocks/runner_mock.go

const (
	// blockingInvocationCutoff defines the maximum age at which a blocking invocation will still
	// get executed. If a blocking invocation is older than this, we're considering it useless to
	// the user and thus won't execute it at all.
	blockingInvocationCutoff = 10 * time.Minute
)

// Writer is an interface for writing messages to Kafka.
type Writer interface {
	WriteMessages(ctx context.Context, msgs ...kafka.Message) error
}

// InnerRunner is an interface for invoking a function and getting an activation.
type InnerRunner interface {
	Invoke(ctx context.Context, logger zerolog.Logger, msg *protocol.ActivationMessage) *entities.Activation
}

type Runner struct {
	InstanceID  int
	KafkaWriter Writer

	// BlockedNamespaces is a Kafka-specific thing, given that it's only relevant due to the buffering
	// we do in Kafka, so it makes sense to have it in here.
	BlockedNamespacesMux sync.RWMutex
	BlockedNamespaces    map[string]struct{}

	InnerRunner InnerRunner
}

func (e *Runner) Invoke(ctx context.Context, logger zerolog.Logger, msg *protocol.ActivationMessage) {
	// Collect fields from all children that receive this context.
	event := logger.Info().Str("invokedVia", "kafka")
	ctx = logging.WithContextEvent(ctx, event)
	defer func() {
		event.Msg("invocation completed")
	}()

	e.BlockedNamespacesMux.RLock()
	_, isNamespaceBlocked := e.BlockedNamespaces[msg.InvocationIdentity.Namespace]
	e.BlockedNamespacesMux.RUnlock()

	// Skip blocking invocations that took longer than the defined invocation cutoff to make it to
	// the invoker. This can happen if requests have been heavily queued.
	skipInvocation := msg.IsBlocking && time.Since(msg.TransactionStart) > blockingInvocationCutoff

	if isNamespaceBlocked || skipInvocation {
		// Add context to the log so we'd know if an activation was skipped and for which reason
		// it was skipped after the fact.
		event.
			Bool("namespaceBlocked", isNamespaceBlocked).
			Bool("skippedOldInvocation", skipInvocation)

		// The namespace is blocked or the invocation request too old, so we don't do any work.
		// We just write an active ack to free the controller capacity.
		if err := e.KafkaWriter.WriteMessages(ctx, kafka.Message{
			Topic: "completed" + msg.RootControllerIndex,
			Value: protocol.MiniCombinedCompletionMessage(e.InstanceID, msg.TransactionID, msg.TransactionStart, msg.ActivationID, false),
		}); err != nil {
			logger.Error().Err(err).Msg("failed to write active ack message")
		}
		return
	}

	// Once we've reached here, we always want to respond to the controller.
	var activation *entities.Activation
	defer func() {
		isSystemError := activation.Response.StatusCode == entities.ActivationStatusCodeSystemError
		topic := "completed" + msg.RootControllerIndex

		var response []byte
		if !msg.IsBlocking {
			response = protocol.MiniCombinedCompletionMessage(e.InstanceID, msg.TransactionID, msg.TransactionStart, msg.ActivationID, isSystemError)
		} else {
			activationBytes, err := json.Marshal(activation)
			if err != nil {
				// If unmarshalling fails for whatever reason, still try to write a fallback message.
				logger.Warn().Err(err).Msg("failed to marshal activation")
				response = protocol.MiniCombinedCompletionMessage(e.InstanceID, msg.TransactionID, msg.TransactionStart, msg.ActivationID, isSystemError)
			} else {
				response = protocol.CombinedCompletionMessage(e.InstanceID, msg.TransactionID, msg.TransactionStart, activationBytes, isSystemError)
			}
		}

		kafkaStart := time.Now()
		if err := e.KafkaWriter.WriteMessages(ctx, kafka.Message{
			Topic: topic,
			Value: response,
		}); err != nil {
			logger.Error().Err(err).Msg("failed to write active ack message")
		}
		logging.ContextEvent(ctx).TimeDiff("kafkaWriteTime", time.Now(), kafkaStart)
	}()

	activation = e.InnerRunner.Invoke(ctx, logger, msg)
}

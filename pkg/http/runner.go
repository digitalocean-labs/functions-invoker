package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"syscall"

	"github.com/rs/zerolog"

	"invoker/pkg/encryption"
	"invoker/pkg/entities"
	"invoker/pkg/logging"
	"invoker/pkg/protocol"
)

// InnerRunner is an interface for invoking a function and getting an activation.
type InnerRunner interface {
	Invoke(ctx context.Context, logger zerolog.Logger, msg *protocol.ActivationMessage) *entities.Activation
}

type Runner struct {
	InstanceID int

	Logger    zerolog.Logger
	Decryptor *encryption.Decryptor

	InnerRunner InnerRunner
}

func (r Runner) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Setup contextual logger. This logger is passed through the entire invocation
	// and all log lines directly related to the invocation will have the respective
	// fields on them.
	loggerCtx := r.Logger.With().
		Str("tid", req.Header.Get("Functions-Transaction-Id")).
		Str("aid", req.Header.Get("Functions-Activation-Id")).
		Str("account", req.Header.Get("Functions-Account")).
		Str("namespace", req.Header.Get("Functions-Namespace")).
		Str("function", req.Header.Get("Functions-Function"))

	logger := loggerCtx.Logger()

	input, err := io.ReadAll(req.Body)
	if err != nil {
		logger.Error().Err(err).Msg("failed to read request body")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	msg, err := protocol.ParseActivationMessage(input, r.Decryptor)
	if err != nil {
		logger.Error().Err(err).Msg("failed to parse activation message")
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Set headers and flush them immediately to inform the loadbalancer about receipt of the invocation.
	// This allows the controller to close the connection early for non-blocking invocation for example.
	w.Header().Add("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	event := logger.Info().Str("invokedVia", "http")
	ctx := logging.WithContextEvent(context.Background(), event)
	defer func() {
		event.Msg("invocation completed")
	}()

	activation := r.InnerRunner.Invoke(ctx, logger, msg)

	isSystemError := activation.Response.StatusCode == entities.ActivationStatusCodeSystemError
	var response []byte
	if !msg.IsBlocking {
		response = protocol.MiniCombinedCompletionMessage(r.InstanceID, msg.TransactionID, msg.TransactionStart, msg.ActivationID, isSystemError)
	} else {
		activationBytes, err := json.Marshal(activation)
		if err != nil {
			// If unmarshalling fails for whatever reason, still try to write a fallback message.
			logger.Warn().Err(err).Msg("failed to marshal activation")
			response = protocol.MiniCombinedCompletionMessage(r.InstanceID, msg.TransactionID, msg.TransactionStart, msg.ActivationID, isSystemError)
		} else {
			response = protocol.CombinedCompletionMessage(r.InstanceID, msg.TransactionID, msg.TransactionStart, activationBytes, isSystemError)
		}
	}

	if _, err := w.Write(response); err != nil {
		event := logger.Error()
		if errors.Is(err, syscall.EPIPE) {
			// Only warn about "broken pipe" errors as the controller doesn't necessarily wait for
			// the requests to finish for async invocations before shutting down. That's fine and benign.
			event = logger.Warn()
		}
		event.Err(err).Msg("failed to write response")
	}
}

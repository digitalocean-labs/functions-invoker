package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"invoker/pkg/clock"
	"invoker/pkg/entities"
	"invoker/pkg/exec"
	"invoker/pkg/logging"
	"invoker/pkg/protocol"
	"invoker/pkg/units"
)

//go:generate mockgen -package mocks -source=runner.go -destination mocks/runner_mock.go

const (
	// minInitDuration is the minimum amount of time we give inits before determining they timed out.
	minInitDuration = 10 * time.Second
	// logCollectionTimeout is a timeout for log collection, in case the runtime is somehow broken
	// and thus not writing the sentinels correctly.
	logCollectionTimeout = 500 * time.Millisecond
)

// ContainerPool is the interface of the container pool that the runner expects.
type ContainerPool interface {
	Get(ctx context.Context, namespace string, fnMeta protocol.ActivationMessageFunction, kind string, memory units.ByteSize) (*exec.PooledContainer, bool, error)
	Put(ctx context.Context, container *exec.PooledContainer)
	PutToDestroy(ctx context.Context, container *exec.PooledContainer) error
}

// Runner executes functions from a given payload.
type Runner struct {
	Clock clock.PassiveClock

	// Pool is the pool of containers to use for the Runner.
	Pool ContainerPool
	// FunctionReader is used to read functions from the database.
	FunctionReader entities.FunctionReader
	// ActivationWriter is used to write activations to the database.
	ActivationWriter entities.ActivationWriter
}

// Invoke invokes a function from the given ActivationMessage and **always** returns an Activation.
func (e *Runner) Invoke(ctx context.Context, logger zerolog.Logger, msg *protocol.ActivationMessage) *entities.Activation {
	contextEvent := logging.ContextEvent(ctx)
	metricActivations.Inc()

	// Start constructing the activation. It's filled in as the invocation goes on to allow us to
	// write a partial activation in error cases.
	qualifiedFnName := fmt.Sprintf("%s/%s", msg.Function.Namespace, msg.Function.Name)
	now := e.Clock.Now()
	activation := &entities.Activation{
		ActivationID: msg.ActivationID,
		Namespace:    msg.InvocationIdentity.Namespace,
		Subject:      msg.InvocationIdentity.Subject,
		Cause:        msg.CausedBy,
		Annotations:  entities.Annotations{},
		Start:        now, // Mutable and will be adjusted later to be closer to the time the function is actually invoked.
		End:          now, // Likewise mutable and will be adjusted below.

		Function: entities.ActivationFunction{
			Name:    msg.Function.Name,
			Version: msg.Function.Version,
			Path:    qualifiedFnName,
			Binding: msg.Function.Binding,
		},

		Response: entities.ActivationResponse{
			// Default to a system error to ensure we're correctly overriding the status codes below.
			StatusCode: entities.ActivationStatusCodeSystemError,
		},
	}

	// If the function had a cause, it must be a sequence.
	if msg.CausedBy != "" {
		activation.Annotations["causedBy"] = "sequence"
	}

	// Add all of the namespace's annotations to the activation.
	for k, v := range msg.InvocationIdentity.Annotations {
		activation.Annotations[k] = v
	}

	// Always write an activation record if we reach here.
	defer func() {
		metricResultSize.Observe(float64(len(activation.Response.Result)))
		switch activation.Response.StatusCode {
		case entities.ActivationStatusCodeSuccess:
			metricStatusSuccess.Inc()
		case entities.ActivationStatusCodeApplicationError:
			metricStatusApplicationError.Inc()
		case entities.ActivationStatusCodeDeveloperError:
			metricStatusDeveloperError.Inc()
		case entities.ActivationStatusCodeSystemError:
			metricStatusSystemError.Inc()
		}

		contextEvent.
			Str("kind", activation.Function.Kind).
			Stringer("activationStatus", activation.Response.StatusCode).
			Float64("gbs", activation.GigabyteSeconds())

		// Only include the actual result if we're looking at either a developer or system error.
		// In those cases we're guaranteed to control the error message and thus its safe to surface it.
		if (activation.Response.StatusCode == entities.ActivationStatusCodeDeveloperError ||
			activation.Response.StatusCode == entities.ActivationStatusCodeSystemError) &&
			activation.Response.Error != "" {
			contextEvent.Str("activationResponse", activation.Response.Error)
		}

		// Write the activation to the database last.
		if !msg.IsBlocking && msg.InvocationIdentity.StoreActivations {
			writeActivationStart := time.Now()
			if err := e.ActivationWriter.WriteActivation(ctx, activation); err != nil {
				logger.Error().Err(err).Msg("failed to write activation")
			}
			contextEvent.Dur("activationWriteTime", time.Since(writeActivationStart))
		}
	}()

	// 1. Fetch the function.
	fn, err := e.FunctionReader.GetFunction(ctx, qualifiedFnName, msg.Function.Revision)
	if err != nil {
		if errors.Is(err, entities.ErrFunctionNotFound) {
			// If the action cannot be found, the user has concurrently deleted it, making this
			// an application error. All other errors are considered system errors and should cause
			// the invoker to be considered unhealthy.
			activation.Response.StatusCode = entities.ActivationStatusCodeApplicationError
			activation.Response.Error = protocol.ErrorFunctionNotFound
			logger.Warn().Err(err).Msg("failed to fetch function (not found)")
			return activation
		}
		activation.Response.StatusCode = entities.ActivationStatusCodeSystemError
		activation.Response.Error = protocol.ErrorFunctionFetch
		logger.Error().Err(err).Msg("failed to fetch function")
		return activation
	}

	fnEntrypoint := fn.Exec.Main
	if fnEntrypoint == "" {
		fnEntrypoint = "main"
	}
	activation.Function.Entrypoint = fnEntrypoint
	activation.Function.Kind = fn.Exec.Kind
	activation.Function.Limits = fn.Limits

	// 2. Get a container for the function.
	container, isReused, err := e.Pool.Get(ctx, msg.InvocationIdentity.Namespace, msg.Function, fn.Exec.Kind, fn.Limits.Memory)
	if err != nil {
		var errContainerBrokenByUser exec.ContainerBrokenDuringReuseError
		if errors.As(err, &errContainerBrokenByUser) {
			// If the container was likely broken by a user, we want to surface the respective cause.
			if errContainerBrokenByUser.State.OOMKilled {
				activation.Response.StatusCode = entities.ActivationStatusCodeDeveloperError
				activation.Response.Error = protocol.ErrorFunctionMemoryExhausted
				return activation
			} else if errContainerBrokenByUser.State.Status == exec.ContainerStateStatusExited {
				activation.Response.StatusCode = entities.ActivationStatusCodeDeveloperError
				activation.Response.Error = protocol.ErrorFunctionAbnormalRun
				return activation
			}
			// Fall through if it's neither of these causes. We're going to then call a system error on this.
		}

		// Since we control all images, we should never fail to bring a container up, making this
		// a system error.
		activation.Response.StatusCode = entities.ActivationStatusCodeSystemError
		activation.Response.Error = protocol.ErrorFunctionProvision
		logger.Error().Err(err).Msg("failed to get container from pool")
		return activation
	}

	var containerBroken bool
	defer func() {
		if containerBroken {
			if err := e.Pool.PutToDestroy(ctx, container); err != nil {
				logger.Error().Err(err).Msg("failed to return broken container to pool")
			}
			return
		}
		e.Pool.Put(ctx, container)
	}()

	// Only add the waitTime annotation if we're not part of a sequence since the transaction's
	// start will be out of whack otherwise.
	if msg.CausedBy == "" {
		waitTime := e.Clock.Since(msg.TransactionStart)
		contextEvent.Dur("waitTime", waitTime)
		activation.WaitTime = waitTime
	}

	// We're about to use the container so from here on, we always want to collect logs to allow
	// for debugging, even for error cases.
	logsCtx, cancelLogCollection := context.WithCancel(ctx)
	logs := make(chan []string)
	go func() {
		defer close(logs)
		logs <- collectLogs(logsCtx, e.Clock, container.Logs(), fn.Limits.Logs)
	}()

	defer func() {
		logCollectionStart := e.Clock.Now()

		// We're not waiting forever for a sentinel, so we potentially have to abort log
		// collection before we find the sentinel.
		timer := time.NewTimer(logCollectionTimeout)
		defer timer.Stop()

		select {
		case activation.Logs = <-logs:
			metricLogCollectionLatency.Observe(e.Clock.Since(logCollectionStart).Seconds())
		case <-timer.C:
			cancelLogCollection()
			activation.Logs = <-logs

			// We've timed out, so add a log collection error before returning.
			logCollectionErrorMessage := protocol.ErrorFunctionLogCollection
			if activation.Response.Error != "" {
				// Use a specific cause message, if there is one.
				logCollectionErrorMessage = punctuate(activation.Response.Error) + " Logs might be missing."
			}
			activation.Logs = append(activation.Logs, formatLog(e.Clock.Now(), exec.LogStreamStderr, logCollectionErrorMessage))

			contextEvent.Bool("logTimeout", true)

			// Failing to read logs until the sentinel puts the container in an unknown state.
			containerBroken = true
			return
		}

		logsDuration := e.Clock.Since(logCollectionStart)
		contextEvent.Dur("logTime", logsDuration)
	}()

	// Allow users to disable API key passing via an annotation. The API key will be passed if the
	// annotation doesn't exist.
	passedAPIKey := msg.InvocationIdentity.APIKey
	if !fn.Annotations.IsTruthy("provide-api-key", true /*valueForNonExistent*/) {
		passedAPIKey = ""
	}

	// This is largely identical between init and run. Only the deadline has to be specifically
	// updated.
	invocationEnvironment := exec.InvocationEnvironment{
		Namespace:       msg.InvocationIdentity.Namespace,
		FunctionName:    "/" + qualifiedFnName,
		FunctionVersion: msg.Function.Version,
		ActivationID:    msg.ActivationID,
		TransactionID:   msg.TransactionID,
		APIKey:          passedAPIKey,
	}

	if !isReused {
		// 3. Initialize the container.
		// We grant at least 10 seconds of timeout for inits, regardless of the function's timeout.
		initTimeout := minInitDuration
		if fn.Limits.Timeout > minInitDuration {
			initTimeout = fn.Limits.Timeout
		}
		initStart := e.Clock.Now()
		deadline := initStart.Add(initTimeout)
		invocationEnvironment.Deadline = deadline
		initCtx, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()

		response, err := container.Initialize(initCtx, exec.ContainerInitPayload{
			Name:        fn.Name,
			IsBinary:    fn.Exec.Binary,
			Entrypoint:  fnEntrypoint,
			Code:        fn.Exec.Code.Resolved,
			Environment: invocationEnvironment,
			Parameters:  msg.InitParameters,
		})

		initEnd := e.Clock.Now()
		initDuration := initEnd.Sub(initStart)
		contextEvent.Dur("initTime", initDuration)

		// Fill in timing data up until now, in case there's an error.
		activation.InitTime = initDuration
		activation.Start = initStart
		activation.End = initEnd

		if err != nil {
			// Failure to initialize always causes us to recreate the container and are all considered
			// developer errors.
			containerBroken = true
			activation.Response.StatusCode = entities.ActivationStatusCodeDeveloperError

			var errResponseLimitExceeded exec.ResponseLimitExceededError
			if errors.As(err, &errResponseLimitExceeded) {
				activation.Response.Error = protocol.ErrorFunctionTruncatedResponse(response, errResponseLimitExceeded.ContentLength, units.ByteSize(len(response)))
				return activation
			}

			// Anything else is either a timeout or a connection error, both hinting at a potential OOM
			// or crash otherwise, so check for that.
			if state, err := container.State(ctx); err != nil {
				logger.Error().Err(err).Msg("failed to get container state")
			} else if state.OOMKilled {
				activation.Response.Error = protocol.ErrorFunctionMemoryExhausted
				return activation
			} else if state.Status == exec.ContainerStateStatusExited {
				activation.Response.Error = protocol.ErrorFunctionAbnormalRun
				return activation
			}

			// It might be a timeout...
			if errors.Is(err, context.DeadlineExceeded) {
				activation.IsTimeout = true
				activation.Response.Error = protocol.ErrorFunctionInitTimeout(initTimeout)
				return activation
			}

			// ...or the runtime returned an actual error to us...
			if len(response) > 0 {
				if errorMessage, err := parseErrorFromResponse(response); err != nil {
					logger.Error().Err(err).Bytes("response", response).Msg("failed to parse error returned from the runtime")
				} else {
					activation.Response.Error = errorMessage
					return activation
				}
			}

			// ...or a generic connection, I/O etc. error.
			activation.Response.Error = protocol.ErrorFunctionAbnormalInit
			return activation
		}
		metricInitLatency.Observe(initDuration.Seconds())
	}

	// 4. Run the container.
	runStart := e.Clock.Now()
	deadline := runStart.Add(fn.Limits.Timeout)
	invocationEnvironment.Deadline = deadline
	runCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	response, err := container.Run(runCtx, exec.ContainerRunPayload{
		Environment: invocationEnvironment,
		Parameters:  msg.Parameters,
	})

	runEnd := e.Clock.Now()
	runDuration := runEnd.Sub(runStart)
	contextEvent.Dur("runTime", runDuration)

	activation.Start = runStart.Add(-activation.InitTime)
	activation.End = runEnd

	if err != nil {
		var errResponseLimitExceeded exec.ResponseLimitExceededError
		if errors.As(err, &errResponseLimitExceeded) {
			activation.Response.StatusCode = entities.ActivationStatusCodeApplicationError
			activation.Response.Error = protocol.ErrorFunctionTruncatedResponse(response, errResponseLimitExceeded.ContentLength, units.ByteSize(len(response)))
			return activation
		}

		// All other errors are developer errors and require container recreation.
		containerBroken = true
		activation.Response.StatusCode = entities.ActivationStatusCodeDeveloperError

		// Anything else is either a timeout or a connection error, both hinting at a potential OOM
		// so check that first.
		if state, err := container.State(ctx); err != nil {
			logger.Error().Err(err).Msg("failed to get container state")
		} else if state.OOMKilled {
			activation.Response.Error = protocol.ErrorFunctionMemoryExhausted
			return activation
		} else if state.Status == exec.ContainerStateStatusExited {
			activation.Response.Error = protocol.ErrorFunctionAbnormalRun
			return activation
		}

		// It might be a timeout...
		if errors.Is(err, context.DeadlineExceeded) {
			activation.IsTimeout = true
			activation.Response.Error = protocol.ErrorFunctionTimeout(fn.Limits.Timeout)
			return activation
		}

		// ...or the runtime returned an actual error to us...
		if len(response) > 0 {
			if errorMessage, err := parseErrorFromResponse(response); err != nil {
				logger.Error().Err(err).Bytes("response", response).Msg("failed to parse error returned from the runtime")
			} else {
				activation.Response.Error = errorMessage
				return activation
			}
		}

		// ...or a generic connection, I/O etc. error.
		activation.Response.Error = protocol.ErrorFunctionAbnormalRun
		return activation
	}
	metricRunLatency.Observe(runDuration.Seconds())

	var parsedResult map[string]json.RawMessage
	if err := json.Unmarshal(response, &parsedResult); err != nil {
		// Failing to parse the response or failing to get it as an object indicates that the user
		// didn't return a valid JSON object. Their fault.
		// This isn't supposed to happen as the runtime should make sure that an object is returned.
		// OPTIM: We should consolidate to the check in here instead.
		activation.Response.StatusCode = entities.ActivationStatusCodeDeveloperError
		activation.Response.Error = protocol.ErrorFunctionInvalidRunResponse(response)
		return activation
	}

	// Now we finally know we have a valid response.
	activation.Response.Result = response
	if parsedResult == nil {
		// Some runtimes, like Golang, return "null" on nil returns and thelike.
		// That's fine, but the API expects JSON objects in all cases, so transform "null"
		// responses to an empty object.
		activation.Response.Result = []byte("{}")
	}

	activation.Response.StatusCode = entities.ActivationStatusCodeSuccess
	// If an error property exists, we consider the activation an application error.
	if _, ok := parsedResult["error"]; ok {
		activation.Response.StatusCode = entities.ActivationStatusCodeApplicationError
	}

	return activation
}

// collectLogs continuously collects logs from the given channel and collects them into a slice
// until the given limit is reached. It flushes the collected logs once it sees the log sentinels
// or when the given context is canceled.
func collectLogs(ctx context.Context, timer clock.PassiveClock, rawLogs <-chan exec.LogLine, limit units.ByteSize) []string {
	var logs []string
	var logsCollected units.ByteSize
	defer func() { metricLogSize.Observe(float64(logsCollected)) }()

	var logsExceeded bool
	var stdoutSentinelFound, stderrSentinelFound bool
	for !stdoutSentinelFound || !stderrSentinelFound {
		select {
		case line := <-rawLogs:
			if line.IsSentinel {
				if line.Stream == exec.LogStreamStdout {
					stdoutSentinelFound = true
				} else {
					stderrSentinelFound = true
				}
				continue
			}

			if logsExceeded {
				continue
			}

			formatted := formatLog(line.Time, line.Stream, line.Message)
			if logsCollected+units.ByteSize(len(formatted)) > limit {
				logs = append(logs, formatLog(timer.Now(), exec.LogStreamStderr, protocol.ErrorFunctionTruncatedLogs(limit)))
				logsExceeded = true
				continue
			}
			logsCollected += units.ByteSize(len(formatted))
			logs = append(logs, formatted)
		case <-ctx.Done():
			return logs
		}
	}
	return logs
}

func formatLog(time time.Time, stream string, msg string) string {
	return fmt.Sprintf("%-30s %s: %s", time.Format("2006-01-02T15:04:05.999999999Z07:00"), stream, msg)
}

// punctuate adds a dot to the end of the message, if not already present.
func punctuate(msg string) string {
	if strings.HasSuffix(msg, ".") {
		return msg
	}
	return msg + "."
}

// runtimeErrorResponse is a response of the runtime containing an error.
type runtimeErrorResponse struct {
	Error string `json:"error"`
}

// parseErrorFromResponse parses the response of the runtime and tries to return the respective
// error message. Fails with an error if the response isn't parseable or doesn't contain an error.
func parseErrorFromResponse(response []byte) (string, error) {
	var parsed runtimeErrorResponse
	if err := json.Unmarshal(response, &parsed); err != nil {
		return "", fmt.Errorf("failed to parse response as JSON: %w", err)
	}
	if parsed.Error == "" {
		return "", errors.New("error response did not contain error field")
	}
	return parsed.Error, nil
}

package exec

import (
	"context"
	"fmt"
	"time"

	"invoker/pkg/units"
)

//go:generate mockgen -package exec -source=container.go -destination container_mock.go

// ResponseLimitExceededError is the error returned if a response was bigger than the allowed limit.
type ResponseLimitExceededError struct {
	// ContentLength is the length of the response that we truncated from.
	ContentLength units.ByteSize
}

// Error implements error.
func (r ResponseLimitExceededError) Error() string {
	return fmt.Sprintf("response of %d bytes exceeded the response limit", r.ContentLength)
}

const (
	// ResponseLimit is the maximum amount of bytes we read as a response from the container.
	ResponseLimit = 1 * units.Megabyte
	// LogStreamStdout is the name of the stdout stream.
	LogStreamStdout = "stdout"
	// LogStreamStderr is the name of the stderr stream.
	LogStreamStderr = "stderr"
	// MaxLogLimit specifies the maximum log limit a user can configure.
	MaxLogLimit = 256 * units.Kilobyte
)

const (
	// ContainerStateStatusExited describes a container that's exited.
	ContainerStateStatusExited = "exited"
)

// LogLine is a single line of log as produced by the container.
type LogLine struct {
	// Message is the message of the logline.
	Message string
	// Stream is the stream the line was produced to (either stdout or stderr).
	Stream string
	// Time is the time at which the line was produced.
	Time time.Time
	// IsSentinel specifies whether the line is a log sentinel, signifying the end of the stream
	// in the context of an invocation.
	IsSentinel bool
}

// ContainerState encapsulates a summary of the container's current state. It's mostly a copy of the
// struct returned by docker inspect.
type ContainerState struct {
	// Status is a representation of the container state. Can be one of "created", "running",
	// "paused", "restarting", "removing", "exited", or "dead"
	Status string
	// Running is true if the container is running fine.
	Running bool
	// Paused is true if the container is paused.
	Paused bool
	// OOMKilled is true if the container got killed by the OOMKiller.
	OOMKilled bool
	// Dead is true if the container is in a dead state.
	Dead bool
	// ExitCode is the exit code of the main process, if applicable.
	ExitCode int
	// Error is the last known error related to the container.
	Error string
	// StartedAt is the timestamp when the container started.
	StartedAt string
	// FinishedAt is the timestamp when the container finished.
	FinishedAt string
}

// Container is a resource that can execute functions.
type Container interface {
	// ID returns an identifier for the given container.
	ID() string
	// IP returns the IP of the given container.
	IP() string
	// Logs returns a channel over all loglines of the container. The lines are written to the channel
	// as they happen and its expected they are consumed as quickly as possible.
	Logs() <-chan LogLine
	// Initialize initializes the container. Called only once.
	//
	// A response is tried to return even if an error is returned. It might contain the allowed bytes
	// from a response that exceeded the limit for example.
	Initialize(ctx context.Context, payload ContainerInitPayload) ([]byte, error)
	// Run runs the function in the container. Called multiple times.
	//
	// A response is tried to return even if an error is returned. It might contain the allowed bytes
	// from a response that exceeded the limit for example.
	Run(ctx context.Context, payload ContainerRunPayload) ([]byte, error)
	// State returns the state of the current container. This can be used to determine if it crashed
	// or ran out of memory for example.
	State(ctx context.Context) (ContainerState, error)
	// Pause pauses the container.
	Pause(ctx context.Context) error
	// Unpause unpauses the container.
	Unpause(ctx context.Context) error
	// Remove removes the container and cleans up all resources.
	Remove(ctx context.Context) error

	// PauseEventually schedules a pause operation to be done eventually after the given grace period
	// has passed. The grace period will be aborted by UnpauseIfNeeded.
	PauseEventually(pauseGrace time.Duration)
	// UnpauseIfNeeded potentially aborts a scheduled pause request from PauseEventually and unpauses
	// the container immediately, if necessary.
	UnpauseIfNeeded(ctx context.Context) error
}

// ContainerFactory defines an interface for creating containers.
type ContainerFactory interface {
	// CreateContainer creates a container.
	CreateContainer(ctx context.Context, kind string, memory units.ByteSize, name string) (Container, error)
}

// CleanupContainer removes the container and constructs an error message that contains both the
// initial error and any potential errors happening during the cleanup itself.
func CleanupContainer(ctx context.Context, c Container, err error) error {
	if state, stateErr := c.State(ctx); stateErr == nil {
		err = fmt.Errorf("%w, state = %+v", err, state)
	}
	if rmErr := c.Remove(ctx); rmErr != nil {
		return fmt.Errorf("%w. The subsequent remove failed with %w", err, rmErr)
	}
	return err
}

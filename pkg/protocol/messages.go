package protocol

import (
	"fmt"
	"time"

	"invoker/pkg/units"
)

const (
	// ErrorFunctionFetch is returned if a function couldn't be fetched.
	ErrorFunctionFetch = "Function could not be fetched."
	// ErrorFunctionRemovedDuringInvocation is returned if a function couldn't be found.
	ErrorFunctionNotFound = "Function could not be found or may have been deleted."
	// ErrorFunctionProvision is returned if resources for a function invocation couldn't be
	// provisioned (i.e. a container didn't come up).
	ErrorFunctionProvision = "Failed to provision resources to run the function."
	// ErrorFunctionAbnormalInit is returned if initialization fails abnormally.
	ErrorFunctionAbnormalInit = "The function did not initialize and exited unexpectedly."
	// ErrorFunctionAbnormalRun is returned if an invocation fails abnormally.
	ErrorFunctionAbnormalRun = "The function did not produce a valid response and exited unexpectedly."
	// ErrorFunctionMemoryExhausted is returned when a function OOMs.
	ErrorFunctionMemoryExhausted = "The function exhausted its memory and was aborted."
	// ErrorFunctionLogCollection is returned when logs couldn't successfully be collected
	// (i.e. due to missing log sentinels.)
	ErrorFunctionLogCollection = "There was an issue while collecting your logs. Logs might be missing."
)

// ErrorFunctionTimeout is returned when a function timed out during initialization.
func ErrorFunctionInitTimeout(timeout time.Duration) string {
	return fmt.Sprintf("The function exceeded its time limits of %d milliseconds during initialization.", timeout.Milliseconds())
}

// ErrorFunctionTimeout is returned when a function timed out during invocation.
func ErrorFunctionTimeout(timeout time.Duration) string {
	return fmt.Sprintf("The function exceeded its time limits of %d milliseconds.", timeout.Milliseconds())
}

// ErrorFunctionTruncatedResponse is returned when a function returns too large of a response.
func ErrorFunctionTruncatedResponse(truncated []byte, got units.ByteSize, allowed units.ByteSize) string {
	return fmt.Sprintf("The function produced a response that exceeded the allowed length: %d > %d bytes. The truncated response was: %s", got, allowed, truncated)
}

// ErrorFunctionTruncatedResponse is written when a function returns too many logs.
func ErrorFunctionTruncatedLogs(allowed units.ByteSize) string {
	return fmt.Sprintf("Logs were truncated because the total bytes size exceeds the limit of %d bytes.", allowed)
}

// ErrorFunctionInvalidRunResponse is returned when a function doesn't return a proper JSON object.
func ErrorFunctionInvalidRunResponse(resp []byte) string {
	return fmt.Sprintf("The function did not produce a valid JSON response: %s", resp)
}

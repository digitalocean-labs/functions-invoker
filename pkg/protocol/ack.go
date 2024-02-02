package protocol

import (
	"fmt"
	"time"
)

// MiniCombinedCompletionMessage construct a message to be written to an OpenWhisk controller
// for cases where a split-write is not necessary. Generally, those cases are non-blocking
// invocations or error cases.
//
// The "Mini" prefix denotes that the message does not contain an entire activation record
// (as it would be required for blocking invocation) but only the activation ID to allow for
// correlation in the controller.
//
// Note: The userMemory field is only looked at in PingMessages. Thus, we cut it out here.
func MiniCombinedCompletionMessage(instance int, tid string, transactionStart time.Time, aid string, isSystemError bool) []byte {
	return CombinedCompletionMessage(instance, tid, transactionStart, []byte(fmt.Sprintf("%q", aid)), isSystemError)
}

// CombinedCompletionMessage construct a message to be written to an OpenWhisk controller
// for cases where a split-write is not necessary. Generally, those cases are non-blocking
// invocations or error cases.
//
// Note: The userMemory field is only looked at in PingMessages. Thus, we cut it out here.
func CombinedCompletionMessage(instance int, tid string, transactionStart time.Time, activation []byte, isSystemError bool) []byte {
	return []byte(fmt.Sprintf(`{"transid":[%q,%d],"response":%s,"isSystemError":%t,"invoker":{"instance":%d,"userMemory":"0 g"}}`, tid, transactionStart.UnixMilli(), activation, isSystemError, instance))
}

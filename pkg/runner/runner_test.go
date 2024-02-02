package runner

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	gomock "github.com/golang/mock/gomock"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"invoker/pkg/clock"
	"invoker/pkg/entities"
	entitiesmocks "invoker/pkg/entities/mocks"
	"invoker/pkg/exec"
	"invoker/pkg/protocol"
	runnermocks "invoker/pkg/runner/mocks"
	"invoker/pkg/units"
)

//go:generate mockgen -package mocks -source=runner.go -destination mocks/runner_mock.go

func TestRunnerInvoke(t *testing.T) {
	t.Parallel()

	// Setup a function to be used in the test.
	fn := &entities.Function{
		Namespace: "functionns",
		Name:      "testfunction",
		Annotations: entities.Annotations{
			// This is set to false by default through the controller.
			"provide-api-key": false,
		},
		Exec: entities.Exec{
			Kind: "nodejs:14",
			Code: entities.FunctionCode{
				Resolved: "thisistheactualcode",
			},
			Main: "fooFunction",
		},
		Limits: entities.Limits{
			Timeout: 3 * time.Second,
			Memory:  11 * units.Megabyte,
			Logs:    64 * units.Byte,
		},
	}

	// This message will be used as the invocation driver for the tests.
	msg := &protocol.ActivationMessage{
		ActivationID:  "testactivationid",
		TransactionID: "testtransactionid",
		InvocationIdentity: protocol.InvocationIdentity{
			Subject:   "invocationsubject",
			Namespace: "invocationns",
			Annotations: map[string]string{
				"namespaceAnnotationKey": "namespaceAnnotationValue",
			},
			APIKey:           "testapikey",
			StoreActivations: true,
		},
		Function: protocol.ActivationMessageFunction{
			Namespace: fn.Namespace,
			Name:      fn.Name,
			Revision:  "testrevision",
			Version:   "0.0.5",
		},
		InitParameters: map[string]json.RawMessage{
			"initParam": []byte(`5`),
		},
		Parameters: map[string]json.RawMessage{
			"runParam": []byte(`8`),
		},
	}

	initPayload := exec.ContainerInitPayload{
		Name:       fn.Name,
		Entrypoint: fn.Exec.Main,
		Code:       fn.Exec.Code.Resolved,
		Environment: exec.InvocationEnvironment{
			ActivationID:    msg.ActivationID,
			TransactionID:   msg.TransactionID,
			Namespace:       msg.InvocationIdentity.Namespace,
			FunctionName:    "/functionns/testfunction",
			FunctionVersion: msg.Function.Version,
			// We expect the init deadline to be 10s rather than 3s as we give a minimum of 10s.
			Deadline: time.Time{}.Add(10 * time.Second),
		},
		Parameters: map[string]json.RawMessage{
			"initParam": []byte(`5`),
		},
	}

	runPayload := exec.ContainerRunPayload{
		Environment: exec.InvocationEnvironment{
			ActivationID:    msg.ActivationID,
			TransactionID:   msg.TransactionID,
			Namespace:       msg.InvocationIdentity.Namespace,
			FunctionName:    "/functionns/testfunction",
			FunctionVersion: msg.Function.Version,
			// Here, we expect the deadline to actually be 3s.
			Deadline: time.Time{}.Add(3 * time.Second),
		},
		Parameters: map[string]json.RawMessage{
			"runParam": []byte(`8`),
		},
	}

	// A few helpers to construct the resulting activation.

	fromBaseActivation := func(f func(*entities.Activation)) *entities.Activation {
		a := &entities.Activation{
			ActivationID: msg.ActivationID,
			Namespace:    msg.InvocationIdentity.Namespace,
			Subject:      msg.InvocationIdentity.Subject,
			Annotations: entities.Annotations{
				"namespaceAnnotationKey": "namespaceAnnotationValue",
			},
			Function: entities.ActivationFunction{
				Name:    fn.Name,
				Version: msg.Function.Version,
				Path:    "functionns/testfunction",
			},
		}
		f(a)
		return a
	}

	fromActivationWithFn := func(f func(*entities.Activation)) *entities.Activation {
		return fromBaseActivation(func(a *entities.Activation) {
			a.Function.Kind = fn.Exec.Kind
			a.Function.Entrypoint = fn.Exec.Main
			a.Function.Limits = fn.Limits
			f(a)
		})
	}

	fromActivationWithLogs := func(f func(*entities.Activation)) *entities.Activation {
		return fromActivationWithFn(func(a *entities.Activation) {
			a.Logs = []string{"0001-01-01T00:00:00Z           stdout: log message"}
			f(a)
		})
	}

	// Deduplicate commonly used mocks setups.

	successfulGetFunction := func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader) {
		fnReader.EXPECT().GetFunction(ctx, "functionns/testfunction", msg.Function.Revision).Return(fn, nil)
	}

	successfulPoolGet := func(ctx context.Context, pool *runnermocks.MockContainerPool, container *exec.PooledContainer, isReused bool) {
		pool.EXPECT().Get(ctx, msg.InvocationIdentity.Namespace, protocol.ActivationMessageFunction{
			Namespace: fn.Namespace,
			Name:      fn.Name,
			Revision:  msg.Function.Revision,
			Version:   msg.Function.Version,
		}, fn.Exec.Kind, fn.Limits.Memory).Return(container, isReused, nil)
	}

	successfulContainerLogs := func(container *exec.MockContainer) {
		logs := make(chan exec.LogLine, 3)
		logs <- exec.LogLine{Stream: exec.LogStreamStdout, Message: "log message"}
		logs <- exec.LogLine{Stream: exec.LogStreamStdout, IsSentinel: true}
		logs <- exec.LogLine{Stream: exec.LogStreamStderr, IsSentinel: true}

		container.EXPECT().Logs().Return(logs)
	}

	tests := []struct {
		name  string
		prep  func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer)
		input *protocol.ActivationMessage
		want  *entities.Activation
	}{{
		name:  "initialize and run successfully",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)

			pooledContainer := &exec.PooledContainer{Container: container}
			successfulPoolGet(ctx, pool, pooledContainer, false /*isReused*/)
			pool.EXPECT().Put(ctx, pooledContainer)

			successfulContainerLogs(container)
			container.EXPECT().Initialize(gomock.Any(), initPayload).Return(nil, nil)
			container.EXPECT().Run(gomock.Any(), runPayload).Return([]byte(`{"foo":"bar"}`), nil)
		},
		want: fromActivationWithLogs(func(a *entities.Activation) {
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeSuccess,
				Result:     []byte(`{"foo":"bar"}`),
			}
		}),
	}, {
		name:  "function getting fails with not found",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			fnReader.EXPECT().GetFunction(ctx, "functionns/testfunction", msg.Function.Revision).Return(nil, entities.ErrFunctionNotFound)
		},
		want: fromBaseActivation(func(a *entities.Activation) {
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeApplicationError,
				Error:      protocol.ErrorFunctionNotFound,
			}
		}),
	}, {
		name:  "function getting fails with a system error",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			fnReader.EXPECT().GetFunction(ctx, "functionns/testfunction", msg.Function.Revision).Return(nil, assert.AnError)
		},
		want: fromBaseActivation(func(a *entities.Activation) {
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeSystemError,
				Error:      protocol.ErrorFunctionFetch,
			}
		}),
	}, {
		name:  "container bringup fails",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)

			pool.EXPECT().
				Get(ctx, msg.InvocationIdentity.Namespace, protocol.ActivationMessageFunction{
					Namespace: fn.Namespace,
					Name:      fn.Name,
					Revision:  msg.Function.Revision,
					Version:   msg.Function.Version,
				}, fn.Exec.Kind, fn.Limits.Memory).Return(nil, false, assert.AnError)
		},
		want: fromActivationWithFn(func(a *entities.Activation) {
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeSystemError,
				Error:      protocol.ErrorFunctionProvision,
			}
		}),
	}, {
		name:  "container broken by an out of band OOM",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)

			pool.EXPECT().
				Get(ctx, msg.InvocationIdentity.Namespace, protocol.ActivationMessageFunction{
					Namespace: fn.Namespace,
					Name:      fn.Name,
					Revision:  msg.Function.Revision,
					Version:   msg.Function.Version,
				}, fn.Exec.Kind, fn.Limits.Memory).Return(nil, false, exec.ContainerBrokenDuringReuseError{State: exec.ContainerState{OOMKilled: true}})
		},
		want: fromActivationWithFn(func(a *entities.Activation) {
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeDeveloperError,
				Error:      protocol.ErrorFunctionMemoryExhausted,
			}
		}),
	}, {
		name:  "container broken by an out of band exit",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)

			pool.EXPECT().
				Get(ctx, msg.InvocationIdentity.Namespace, protocol.ActivationMessageFunction{
					Namespace: fn.Namespace,
					Name:      fn.Name,
					Revision:  msg.Function.Revision,
					Version:   msg.Function.Version,
				}, fn.Exec.Kind, fn.Limits.Memory).Return(nil, false, exec.ContainerBrokenDuringReuseError{State: exec.ContainerState{Status: exec.ContainerStateStatusExited}})
		},
		want: fromActivationWithFn(func(a *entities.Activation) {
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeDeveloperError,
				Error:      protocol.ErrorFunctionAbnormalRun,
			}
		}),
	}, {
		name:  "initialize fails with exceeding response",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)
			pooledContainer := &exec.PooledContainer{Container: container}
			successfulPoolGet(ctx, pool, pooledContainer, false /*isReused*/)
			successfulContainerLogs(container)

			container.EXPECT().Initialize(gomock.Any(), initPayload).Return([]byte(`afewtruncatedbytes`), exec.ResponseLimitExceededError{ContentLength: 19})
			pool.EXPECT().PutToDestroy(ctx, pooledContainer)
		},
		want: fromActivationWithLogs(func(a *entities.Activation) {
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeDeveloperError,
				Error:      protocol.ErrorFunctionTruncatedResponse([]byte(`afewtruncatedbytes`), 19, 18),
			}
		}),
	}, {
		name:  "initialize fails with OOM",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)
			pooledContainer := &exec.PooledContainer{Container: container}
			successfulPoolGet(ctx, pool, pooledContainer, false /*isReused*/)
			successfulContainerLogs(container)

			container.EXPECT().Initialize(gomock.Any(), initPayload).Return(nil, assert.AnError)
			container.EXPECT().State(ctx).Return(exec.ContainerState{OOMKilled: true}, nil)
			pool.EXPECT().PutToDestroy(ctx, pooledContainer)
		},
		want: fromActivationWithLogs(func(a *entities.Activation) {
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeDeveloperError,
				Error:      protocol.ErrorFunctionMemoryExhausted,
			}
		}),
	}, {
		name:  "initialize fails with exit",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)
			pooledContainer := &exec.PooledContainer{Container: container}
			successfulPoolGet(ctx, pool, pooledContainer, false /*isReused*/)
			successfulContainerLogs(container)

			container.EXPECT().Initialize(gomock.Any(), initPayload).Return(nil, assert.AnError)
			container.EXPECT().State(ctx).Return(exec.ContainerState{Status: exec.ContainerStateStatusExited}, nil)
			pool.EXPECT().PutToDestroy(ctx, pooledContainer)
		},
		want: fromActivationWithLogs(func(a *entities.Activation) {
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeDeveloperError,
				Error:      protocol.ErrorFunctionAbnormalRun,
			}
		}),
	}, {
		name:  "initialize fails with timeout",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)
			pooledContainer := &exec.PooledContainer{Container: container}
			successfulPoolGet(ctx, pool, pooledContainer, false /*isReused*/)
			successfulContainerLogs(container)

			container.EXPECT().Initialize(gomock.Any(), initPayload).Return(nil, context.DeadlineExceeded)
			container.EXPECT().State(ctx).Return(exec.ContainerState{OOMKilled: false}, nil)
			pool.EXPECT().PutToDestroy(ctx, pooledContainer)
		},
		want: fromActivationWithLogs(func(a *entities.Activation) {
			a.IsTimeout = true
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeDeveloperError,
				// Note that this says 10 seconds due to the added timeout on inits.
				Error: protocol.ErrorFunctionInitTimeout(10 * time.Second),
			}
		}),
	}, {
		name:  "initialize fails with returning an error",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)
			pooledContainer := &exec.PooledContainer{Container: container}
			successfulPoolGet(ctx, pool, pooledContainer, false /*isReused*/)
			successfulContainerLogs(container)

			container.EXPECT().Initialize(gomock.Any(), initPayload).Return([]byte(`{"error":"anerror"}`), assert.AnError)
			container.EXPECT().State(ctx).Return(exec.ContainerState{OOMKilled: false}, nil)
			pool.EXPECT().PutToDestroy(ctx, pooledContainer)
		},
		want: fromActivationWithLogs(func(a *entities.Activation) {
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeDeveloperError,
				Error:      "anerror",
			}
		}),
	}, {
		name:  "initialize fails with returning an unparseable error",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)
			pooledContainer := &exec.PooledContainer{Container: container}
			successfulPoolGet(ctx, pool, pooledContainer, false /*isReused*/)
			successfulContainerLogs(container)

			container.EXPECT().Initialize(gomock.Any(), initPayload).Return([]byte(`anerror`), assert.AnError)
			container.EXPECT().State(ctx).Return(exec.ContainerState{OOMKilled: false}, nil)
			pool.EXPECT().PutToDestroy(ctx, pooledContainer)
		},
		want: fromActivationWithLogs(func(a *entities.Activation) {
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeDeveloperError,
				Error:      protocol.ErrorFunctionAbnormalInit,
			}
		}),
	}, {
		name:  "initialize fails with a generic error",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)
			pooledContainer := &exec.PooledContainer{Container: container}
			successfulPoolGet(ctx, pool, pooledContainer, false /*isReused*/)
			successfulContainerLogs(container)

			container.EXPECT().Initialize(gomock.Any(), initPayload).Return(nil, assert.AnError)
			container.EXPECT().State(ctx).Return(exec.ContainerState{OOMKilled: false}, nil)
			pool.EXPECT().PutToDestroy(ctx, pooledContainer)
		},
		want: fromActivationWithLogs(func(a *entities.Activation) {
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeDeveloperError,
				Error:      protocol.ErrorFunctionAbnormalInit,
			}
		}),
	}, {
		name:  "run fails with exceeding response",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)
			pooledContainer := &exec.PooledContainer{Container: container}
			successfulPoolGet(ctx, pool, pooledContainer, true /*isReused*/)
			successfulContainerLogs(container)

			container.EXPECT().Run(gomock.Any(), runPayload).Return([]byte(`afewtruncatedbytes`), exec.ResponseLimitExceededError{ContentLength: 19})
			// Note that we're not deleting the container here!
			pool.EXPECT().Put(ctx, pooledContainer)
		},
		want: fromActivationWithLogs(func(a *entities.Activation) {
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeApplicationError,
				Error:      protocol.ErrorFunctionTruncatedResponse([]byte(`afewtruncatedbytes`), 19, 18),
			}
		}),
	}, {
		name:  "run fails with OOM",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)
			pooledContainer := &exec.PooledContainer{Container: container}
			successfulPoolGet(ctx, pool, pooledContainer, true /*isReused*/)
			successfulContainerLogs(container)

			container.EXPECT().Run(gomock.Any(), runPayload).Return(nil, assert.AnError)
			container.EXPECT().State(ctx).Return(exec.ContainerState{OOMKilled: true}, nil)
			pool.EXPECT().PutToDestroy(ctx, pooledContainer)
		},
		want: fromActivationWithLogs(func(a *entities.Activation) {
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeDeveloperError,
				Error:      protocol.ErrorFunctionMemoryExhausted,
			}
		}),
	}, {
		name:  "run fails with exit",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)
			pooledContainer := &exec.PooledContainer{Container: container}
			successfulPoolGet(ctx, pool, pooledContainer, true /*isReused*/)
			successfulContainerLogs(container)

			container.EXPECT().Run(gomock.Any(), runPayload).Return(nil, assert.AnError)
			container.EXPECT().State(ctx).Return(exec.ContainerState{Status: exec.ContainerStateStatusExited}, nil)
			pool.EXPECT().PutToDestroy(ctx, pooledContainer)
		},
		want: fromActivationWithLogs(func(a *entities.Activation) {
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeDeveloperError,
				Error:      protocol.ErrorFunctionAbnormalRun,
			}
		}),
	}, {
		name:  "run fails with timeout",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)
			pooledContainer := &exec.PooledContainer{Container: container}
			successfulPoolGet(ctx, pool, pooledContainer, true /*isReused*/)
			successfulContainerLogs(container)

			container.EXPECT().Run(gomock.Any(), runPayload).Return(nil, context.DeadlineExceeded)
			container.EXPECT().State(ctx).Return(exec.ContainerState{OOMKilled: false}, nil)
			pool.EXPECT().PutToDestroy(ctx, pooledContainer)
		},
		want: fromActivationWithLogs(func(a *entities.Activation) {
			a.IsTimeout = true
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeDeveloperError,
				Error:      protocol.ErrorFunctionTimeout(3 * time.Second),
			}
		}),
	}, {
		name:  "run fails with returning an error",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)
			pooledContainer := &exec.PooledContainer{Container: container}
			successfulPoolGet(ctx, pool, pooledContainer, true /*isReused*/)
			successfulContainerLogs(container)

			container.EXPECT().Run(gomock.Any(), runPayload).Return([]byte(`{"error":"anerror"}`), assert.AnError)
			container.EXPECT().State(ctx).Return(exec.ContainerState{OOMKilled: false}, nil)
			pool.EXPECT().PutToDestroy(ctx, pooledContainer)
		},
		want: fromActivationWithLogs(func(a *entities.Activation) {
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeDeveloperError,
				Error:      "anerror",
			}
		}),
	}, {
		name:  "run fails with returning an unparseable error",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)
			pooledContainer := &exec.PooledContainer{Container: container}
			successfulPoolGet(ctx, pool, pooledContainer, true /*isReused*/)
			successfulContainerLogs(container)

			container.EXPECT().Run(gomock.Any(), runPayload).Return([]byte(`anerror`), assert.AnError)
			container.EXPECT().State(ctx).Return(exec.ContainerState{OOMKilled: false}, nil)
			pool.EXPECT().PutToDestroy(ctx, pooledContainer)
		},
		want: fromActivationWithLogs(func(a *entities.Activation) {
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeDeveloperError,
				Error:      protocol.ErrorFunctionAbnormalRun,
			}
		}),
	}, {
		name:  "run fails with generic error",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)
			pooledContainer := &exec.PooledContainer{Container: container}
			successfulPoolGet(ctx, pool, pooledContainer, true /*isReused*/)
			successfulContainerLogs(container)

			container.EXPECT().Run(gomock.Any(), runPayload).Return(nil, assert.AnError)
			container.EXPECT().State(ctx).Return(exec.ContainerState{OOMKilled: false}, nil)
			pool.EXPECT().PutToDestroy(ctx, pooledContainer)
		},
		want: fromActivationWithLogs(func(a *entities.Activation) {
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeDeveloperError,
				Error:      protocol.ErrorFunctionAbnormalRun,
			}
		}),
	}, {
		name:  "run returns invalid JSON",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)
			pooledContainer := &exec.PooledContainer{Container: container}
			successfulPoolGet(ctx, pool, pooledContainer, true /*isReused*/)
			successfulContainerLogs(container)

			container.EXPECT().Run(gomock.Any(), runPayload).Return([]byte(`novalidjson`), nil)
			pool.EXPECT().Put(ctx, pooledContainer)
		},
		want: fromActivationWithLogs(func(a *entities.Activation) {
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeDeveloperError,
				Error:      protocol.ErrorFunctionInvalidRunResponse([]byte(`novalidjson`)),
			}
		}),
	}, {
		name:  "run returns invalidly escaped JSON",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)
			pooledContainer := &exec.PooledContainer{Container: container}
			successfulPoolGet(ctx, pool, pooledContainer, true /*isReused*/)
			successfulContainerLogs(container)

			container.EXPECT().Run(gomock.Any(), runPayload).Return([]byte(`{"foo": "\x"}`), nil)
			pool.EXPECT().Put(ctx, pooledContainer)
		},
		want: fromActivationWithLogs(func(a *entities.Activation) {
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeDeveloperError,
				Error:      protocol.ErrorFunctionInvalidRunResponse([]byte(`{"foo": "\x"}`)),
			}
		}),
	}, {
		name:  "run returns null",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)
			pooledContainer := &exec.PooledContainer{Container: container}
			successfulPoolGet(ctx, pool, pooledContainer, true /*isReused*/)
			successfulContainerLogs(container)

			container.EXPECT().Run(gomock.Any(), runPayload).Return([]byte(`null`), nil)
			pool.EXPECT().Put(ctx, pooledContainer)
		},
		want: fromActivationWithLogs(func(a *entities.Activation) {
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeSuccess,
				Result:     []byte("{}"),
			}
		}),
	}, {
		name:  "run returns an application error",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)
			pooledContainer := &exec.PooledContainer{Container: container}
			successfulPoolGet(ctx, pool, pooledContainer, true /*isReused*/)
			successfulContainerLogs(container)

			container.EXPECT().Run(gomock.Any(), runPayload).Return([]byte(`{"error":"foo"}`), nil)
			pool.EXPECT().Put(ctx, pooledContainer)
		},
		want: fromActivationWithLogs(func(a *entities.Activation) {
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeApplicationError,
				Result:     []byte(`{"error":"foo"}`),
			}
		}),
	}, {
		name:  "fails to find log sentinels with no additional hint",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)
			pooledContainer := &exec.PooledContainer{Container: container}
			successfulPoolGet(ctx, pool, pooledContainer, true /*isReused*/)
			container.EXPECT().Run(gomock.Any(), runPayload).Return([]byte(`{"foo":"bar"}`), nil)

			logs := make(chan exec.LogLine, 1)
			logs <- exec.LogLine{Stream: exec.LogStreamStdout, Message: "log message"}
			container.EXPECT().Logs().Return(logs)
			pool.EXPECT().PutToDestroy(ctx, pooledContainer)
		},
		want: fromActivationWithLogs(func(a *entities.Activation) {
			a.Logs = append(a.Logs, "0001-01-01T00:00:00Z           stderr: There was an issue while collecting your logs. Logs might be missing.")
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeSuccess,
				Result:     []byte(`{"foo":"bar"}`),
			}
		}),
	}, {
		name:  "fails to find log sentinels due to a timeout",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)
			pooledContainer := &exec.PooledContainer{Container: container}
			successfulPoolGet(ctx, pool, pooledContainer, true /*isReused*/)

			container.EXPECT().Run(gomock.Any(), runPayload).Return(nil, context.DeadlineExceeded)
			container.EXPECT().State(ctx).Return(exec.ContainerState{OOMKilled: false}, nil)
			logs := make(chan exec.LogLine, 1)
			logs <- exec.LogLine{Stream: exec.LogStreamStdout, Message: "log message"}
			container.EXPECT().Logs().Return(logs)
			pool.EXPECT().PutToDestroy(ctx, pooledContainer)
		},
		want: fromActivationWithLogs(func(a *entities.Activation) {
			a.IsTimeout = true
			a.Logs = append(a.Logs, "0001-01-01T00:00:00Z           stderr: The function exceeded its time limits of 3000 milliseconds. Logs might be missing.")
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeDeveloperError,
				Error:      protocol.ErrorFunctionTimeout(3 * time.Second),
			}
		}),
	}, {
		name:  "fails to find log sentinels due to an OOM",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)
			pooledContainer := &exec.PooledContainer{Container: container}
			successfulPoolGet(ctx, pool, pooledContainer, true /*isReused*/)

			container.EXPECT().Run(gomock.Any(), runPayload).Return(nil, context.DeadlineExceeded)
			container.EXPECT().State(ctx).Return(exec.ContainerState{OOMKilled: true}, nil)
			logs := make(chan exec.LogLine, 1)
			logs <- exec.LogLine{Stream: exec.LogStreamStdout, Message: "log message"}
			container.EXPECT().Logs().Return(logs)
			pool.EXPECT().PutToDestroy(ctx, pooledContainer)
		},
		want: fromActivationWithLogs(func(a *entities.Activation) {
			a.Logs = append(a.Logs, "0001-01-01T00:00:00Z           stderr: The function exhausted its memory and was aborted. Logs might be missing.")
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeDeveloperError,
				Error:      protocol.ErrorFunctionMemoryExhausted,
			}
		}),
	}, {
		name:  "fails to find log sentinels with a specific error",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)
			pooledContainer := &exec.PooledContainer{Container: container}
			successfulPoolGet(ctx, pool, pooledContainer, true /*isReused*/)

			container.EXPECT().Run(gomock.Any(), runPayload).Return([]byte(`{"error":"an error without puncuation"}`), assert.AnError)
			container.EXPECT().State(ctx).Return(exec.ContainerState{OOMKilled: false}, nil)
			logs := make(chan exec.LogLine, 1)
			logs <- exec.LogLine{Stream: exec.LogStreamStdout, Message: "log message"}
			container.EXPECT().Logs().Return(logs)
			pool.EXPECT().PutToDestroy(ctx, pooledContainer)
		},
		want: fromActivationWithLogs(func(a *entities.Activation) {
			a.Logs = append(a.Logs, "0001-01-01T00:00:00Z           stderr: an error without puncuation. Logs might be missing.")
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeDeveloperError,
				Error:      "an error without puncuation",
			}
		}),
	}, {
		name:  "exceeds the log limit",
		input: msg,
		prep: func(ctx context.Context, fnReader *entitiesmocks.MockFunctionReader, pool *runnermocks.MockContainerPool, container *exec.MockContainer) {
			successfulGetFunction(ctx, fnReader)
			pooledContainer := &exec.PooledContainer{Container: container}
			successfulPoolGet(ctx, pool, pooledContainer, true /*isReused*/)
			container.EXPECT().Run(gomock.Any(), runPayload).Return([]byte(`{"foo":"bar"}`), nil)

			logs := make(chan exec.LogLine, 4)
			logs <- exec.LogLine{Stream: exec.LogStreamStdout, Message: "log message"}
			logs <- exec.LogLine{Stream: exec.LogStreamStdout, Message: "log message2"}
			logs <- exec.LogLine{Stream: exec.LogStreamStdout, IsSentinel: true}
			logs <- exec.LogLine{Stream: exec.LogStreamStderr, IsSentinel: true}
			container.EXPECT().Logs().Return(logs)
			// Note that we're not deleting the container because we've still found the sentinels.
			pool.EXPECT().Put(ctx, pooledContainer)
		},
		want: fromActivationWithLogs(func(a *entities.Activation) {
			a.Logs = append(a.Logs, "0001-01-01T00:00:00Z           stderr: Logs were truncated because the total bytes size exceeds the limit of 64 bytes.")
			a.Response = entities.ActivationResponse{
				StatusCode: entities.ActivationStatusCodeSuccess,
				Result:     []byte(`{"foo":"bar"}`),
			}
		}),
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup a unique context to assert we're passing it down all functions correctly.
			var ctxKey struct{}
			ctx := context.WithValue(context.Background(), ctxKey, "bar")

			// Setup all required mocks.
			ctrl := gomock.NewController(t)
			fnReader := entitiesmocks.NewMockFunctionReader(ctrl)
			pool := runnermocks.NewMockContainerPool(ctrl)
			container := exec.NewMockContainer(ctrl)
			activationWriter := entitiesmocks.NewMockActivationWriter(ctrl)

			// ID and IP can be called for logging.
			container.EXPECT().ID().AnyTimes()
			container.EXPECT().IP().AnyTimes()

			tt.prep(ctx, fnReader, pool, container)

			// Activations are always supposed to be written, unless StoreActivations is turned off.
			if tt.input.InvocationIdentity.StoreActivations {
				activationWriter.EXPECT().WriteActivation(ctx, tt.want)
			}

			runner := &Runner{
				Clock:            clock.FakeClock{Time: time.Time{}},
				Pool:             pool,
				FunctionReader:   fnReader,
				ActivationWriter: activationWriter,
			}
			got := runner.Invoke(ctx, zerolog.New(zerolog.NewTestWriter(t)), tt.input)
			require.Equal(t, tt.want, got)
		})
	}
}

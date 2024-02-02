package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	dockerunits "github.com/docker/go-units"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"invoker/pkg/exec"
	"invoker/pkg/units"
)

var (
	successfulCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *specs.Platform, containerName string) (container.CreateResponse, error) {
		return container.CreateResponse{ID: "testid"}, nil
	}
	successfulAttachFn = func(ctx context.Context, container string, options types.ContainerAttachOptions) (types.HijackedResponse, error) {
		return types.HijackedResponse{
			Reader: bufio.NewReader(bytes.NewReader(nil)),
		}, nil
	}
	successfulStartFn = func(ctx context.Context, container string, options types.ContainerStartOptions) error {
		return nil
	}
	successfulRemoveFn = func(ctx context.Context, container string, options types.ContainerRemoveOptions) error {
		return nil
	}
)

func TestDockerContainerFactoryCreateContainer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string

		kind            string
		containerMemory units.ByteSize
		containerName   string

		client    *fakeContainerAPIClient
		transport *fakeTransport

		wantCreateRequest containerCreateRequest
		wantError         error
		wantRemove        bool
	}{{
		name:            "all good",
		kind:            "nodejs:foo",
		containerMemory: 213 * units.Megabyte,
		containerName:   "testcontainer",

		client: &fakeContainerAPIClient{
			createFn: successfulCreateFn,
			attachFn: successfulAttachFn,
			startFn:  successfulStartFn,
		},
		transport: &fakeTransport{
			responses: []roundTripResponse{
				{err: assert.AnError}, // Errors are retried.
				{response: &http.Response{StatusCode: http.StatusBadGateway}}, // As are non 200s.
				{response: &http.Response{StatusCode: http.StatusOK}},
			},
		},

		wantCreateRequest: containerCreateRequest{
			config: &container.Config{
				Image:      "testregistry/testimage:testtag",
				WorkingDir: "/tmp",
				Env:        []string{"HOME=/tmp", "__OW_API_HOST=https://testapihost.com"},
			},
			hostConfig: &container.HostConfig{
				Tmpfs:          map[string]string{"/tmp": "rw,exec,nosuid,nodev,size=213m"},
				CapDrop:        []string{"ALL"},
				LogConfig:      container.LogConfig{Type: "none"},
				SecurityOpt:    []string{"no-new-privileges"},
				ReadonlyRootfs: true,
				Runtime:        "runsc",
				Resources: container.Resources{
					CPUShares:  213,
					Memory:     213000000, // We translate "our" units into docker's units.
					MemorySwap: 213000000,
					PidsLimit:  int64Pointer(1024),
					Ulimits:    []*dockerunits.Ulimit{{Name: "nofile", Soft: 1024, Hard: 1024}},
				},
				DNS: []string{"4.3.3.3"},
				Mounts: []mount.Mount{{
					Type:     mount.TypeBind,
					Source:   "/etc/docker/functions.resolv.conf",
					Target:   "/etc/resolv.conf",
					ReadOnly: true,
				}},
			},
			networkingConfig: &network.NetworkingConfig{
				EndpointsConfig: map[string]*network.EndpointSettings{
					"functions": {
						IPAMConfig: &network.EndpointIPAMConfig{
							IPv4Address: "testIP",
						},
					},
				},
			},
			containerName: "testcontainer",
		},
	}, {
		name:            "error during create",
		kind:            "nodejs:foo",
		containerMemory: 213 * units.Megabyte,
		containerName:   "testcontainer",

		client: &fakeContainerAPIClient{
			createFn: func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *specs.Platform, containerName string) (container.CreateResponse, error) {
				return container.CreateResponse{}, assert.AnError
			},
		},
		wantError: assert.AnError,
		// No remove wanted here as the create already failed.
	}, {
		name:            "error during attach",
		kind:            "nodejs:foo",
		containerMemory: 213 * units.Megabyte,
		containerName:   "testcontainer",

		client: &fakeContainerAPIClient{
			createFn: successfulCreateFn,
			attachFn: func(ctx context.Context, container string, options types.ContainerAttachOptions) (types.HijackedResponse, error) {
				return types.HijackedResponse{}, assert.AnError
			},
			inspectFn: func(ctx context.Context, container string) (types.ContainerJSON, error) {
				return types.ContainerJSON{}, nil
			},
			removeFn: successfulRemoveFn,
		},
		wantError:  assert.AnError,
		wantRemove: true,
	}, {
		name:            "error during start",
		kind:            "nodejs:foo",
		containerMemory: 213 * units.Megabyte,
		containerName:   "testcontainer",

		client: &fakeContainerAPIClient{
			createFn: successfulCreateFn,
			attachFn: successfulAttachFn,
			startFn: func(ctx context.Context, container string, options types.ContainerStartOptions) error {
				return assert.AnError
			},
			inspectFn: func(ctx context.Context, container string) (types.ContainerJSON, error) {
				return types.ContainerJSON{}, nil
			},
			removeFn: successfulRemoveFn,
		},
		wantError:  assert.AnError,
		wantRemove: true,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Wrap the createFn of the client to be able to assert the passed parameters.
			var createRequest containerCreateRequest
			oldCreateFn := tt.client.createFn
			tt.client.createFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *specs.Platform, containerName string) (container.CreateResponse, error) {
				createRequest = containerCreateRequest{
					config:           config,
					hostConfig:       hostConfig,
					networkingConfig: networkingConfig,
					platform:         platform,
					containerName:    containerName,
				}
				return oldCreateFn(ctx, config, hostConfig, networkingConfig, platform, containerName)
			}

			// Wrap the removeFn of the client to be able to assert is has been called in all
			// error cases.
			oldRemoveFn := tt.client.removeFn
			var removeCalled bool
			tt.client.removeFn = func(ctx context.Context, container string, options types.ContainerRemoveOptions) error {
				removeCalled = true
				return oldRemoveFn(ctx, container, options)
			}

			runtimeManifests := map[string]exec.RuntimeManifest{
				"nodejs:foo": {
					Image: exec.RuntimeImage("testregistry/testimage:testtag"),
				},
			}

			factory := &ContainerFactory{
				Client:           tt.client,
				Transport:        tt.transport,
				RuntimeManifests: runtimeManifests,
				APIHost:          "https://testapihost.com",
				DNSServers:       []string{"4.3.3.3"},
				Network:          "functions",
				UsableIPs:        []string{"testIP"},
			}

			_, err := factory.CreateContainer(context.Background(), tt.kind, tt.containerMemory, tt.containerName)
			if tt.wantError != nil {
				require.ErrorIs(t, err, tt.wantError)
				require.Equal(t, tt.wantRemove, removeCalled)
				// If there was an error, the IP should've been released again.
				require.Equal(t, []string{"testIP"}, factory.UsableIPs)
				return
			}
			require.NoError(t, err)

			// This is somewhat of an introspective test as we don't mainly assert the outcome but
			// constructing the create request is what's most interesting here.
			require.Equal(t, tt.wantCreateRequest, createRequest)
			// Assert that all responses have been used up by the test. This indirectly asserts
			// that the bringup logic has done its retries.
			require.Empty(t, tt.transport.responses)
			// If we succeeded creating the container, the IP should be taken now.
			require.Empty(t, factory.UsableIPs)
		})
	}
}

func TestDockerContainerFactoryCreateContainerUnknownKind(t *testing.T) {
	runtimeManifests := map[string]exec.RuntimeManifest{
		"nodejs:foo": {
			Image: exec.RuntimeImage("testregistry/testimage:testtag"),
		},
	}

	factory := &ContainerFactory{
		RuntimeManifests: runtimeManifests,
	}

	_, err := factory.CreateContainer(context.Background(), "unknownKind", 256*units.Megabyte, "testcontainer")
	require.EqualError(t, err, `failed to get runtime manifest for kind "unknownKind" to create container`)
}

func TestContainerPollingEventuallyStops(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	transport := fakeTransportFunc(func(r *http.Request) (*http.Response, error) {
		call := calls.Add(1)
		if call == 1 {
			// First, return an error that's retried.
			return nil, assert.AnError
		}
		// Otherwise, wait for the context to be canceled.
		<-r.Context().Done()
		return nil, r.Context().Err()
	})

	container := &Container{
		httpClient: &http.Client{
			Transport: transport,
		},
	}
	pollCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := container.pollUntilResponding(pollCtx, "http://192.168.0.1:8080/", 1*time.Millisecond)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.EqualValues(t, 2, calls.Load())

	// Make sure the error contains useful debugging information.
	assert.ErrorContains(t, err, "failed to wait for container to respond in 2 tries")
	assert.ErrorContains(t, err, assert.AnError.Error())
}

func TestContainerPollingFailsOnFirstTry(t *testing.T) {
	t.Parallel()

	transport := fakeTransportFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})

	container := &Container{
		httpClient: &http.Client{
			Transport: transport,
		},
	}
	pollCtx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.
	err := container.pollUntilResponding(pollCtx, "http://192.168.0.1:8080/", 1*time.Millisecond)
	assert.ErrorIs(t, err, context.Canceled)

	// Make sure the error contains useful debugging information.
	assert.ErrorContains(t, err, "failed to wait for container to respond on first try")
}

func TestContainerInit(t *testing.T) {
	t.Parallel()

	payload := exec.ContainerInitPayload{
		Name:       "testfunction",
		Entrypoint: "main",
		Code:       "testcode",
	}
	wantPayload, err := json.Marshal(&payload)
	require.NoError(t, err)

	initURL := "http://192.168.0.1:8080/init"

	container := &Container{
		httpClient: &http.Client{
			Transport: fakeTransportFunc(func(r *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)

				// Assert that the request is put together correctly.
				require.Equal(t, initURL, r.URL.String())
				require.Equal(t, wantPayload, body)

				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewBufferString("testbody")),
				}, nil
			}),
		},
		initURL: initURL,
	}

	_, err = container.Initialize(context.Background(), payload)
	require.NoError(t, err)
}

func TestContainerRun(t *testing.T) {
	t.Parallel()

	payload := exec.ContainerRunPayload{
		Parameters: map[string]json.RawMessage{
			"foo": []byte(`"bar"`),
		},
	}
	wantPayload, err := json.Marshal(&payload)
	require.NoError(t, err)

	runURL := "http://192.168.0.1:8080/run"

	container := &Container{
		httpClient: &http.Client{
			Transport: fakeTransportFunc(func(r *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)

				// Assert that the request is put together correctly.
				require.Equal(t, runURL, r.URL.String())
				require.Equal(t, wantPayload, body)

				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewBufferString("testbody")),
				}, nil
			}),
		},
		runURL: runURL,
	}

	_, err = container.Run(context.Background(), payload)
	require.NoError(t, err)
}

func TestContainerInitRunResponseHandling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// response is a function to be able to regenerate the body's readers for subsequent calls.
		response          func() roundTripResponse
		want              []byte
		wantError         bool
		wantSpecificError error
	}{{
		name: "all good",
		response: func() roundTripResponse {
			return roundTripResponse{
				response: &http.Response{
					ContentLength: 8,
					StatusCode:    http.StatusOK,
					Body:          io.NopCloser(bytes.NewBufferString("testbody")),
				},
			}
		},
		want: []byte("testbody"),
	}, {
		name: "unexpected status code",
		response: func() roundTripResponse {
			return roundTripResponse{
				response: &http.Response{
					ContentLength: 9,
					StatusCode:    http.StatusInternalServerError,
					Body:          io.NopCloser(bytes.NewBufferString("errorbody")),
				},
			}
		},
		want:      []byte("errorbody"), // Returned even though we've gotten an error.
		wantError: true,
	}, {
		name: "error from roundtripper",
		response: func() roundTripResponse {
			return roundTripResponse{
				err: assert.AnError,
			}
		},
		wantError:         true,
		wantSpecificError: assert.AnError,
	}, {
		name: "exceed payload limit",
		response: func() roundTripResponse {
			return roundTripResponse{
				response: &http.Response{
					ContentLength: int64(exec.ResponseLimit + 1),
					StatusCode:    http.StatusInternalServerError,
					// Generate a body that exceeds the given limit by 1 byte.
					Body: io.NopCloser(bytes.NewBufferString(strings.Repeat("A", int(exec.ResponseLimit+1)))),
				},
			}
		},
		// We want to see exactly the response limit amount of bytes of the string.
		want:              []byte(strings.Repeat("A", int(exec.ResponseLimit))),
		wantError:         true,
		wantSpecificError: exec.ResponseLimitExceededError{ContentLength: exec.ResponseLimit + 1},
	}}

	for _, tt := range tests {
		t.Run(tt.name+" (init)", func(t *testing.T) {
			container := &Container{
				httpClient: &http.Client{Transport: &fakeTransport{
					responses: []roundTripResponse{tt.response()},
				}},
			}

			got, err := container.Initialize(context.Background(), exec.ContainerInitPayload{})
			if tt.wantError {
				require.Error(t, err)
				if tt.wantSpecificError != nil {
					require.ErrorIs(t, err, tt.wantSpecificError)
				}
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.want, got)
		})

		t.Run(tt.name+" (run)", func(t *testing.T) {
			container := &Container{
				httpClient: &http.Client{Transport: &fakeTransport{
					responses: []roundTripResponse{tt.response()},
				}},
			}

			got, err := container.Run(context.Background(), exec.ContainerRunPayload{})
			if tt.wantError {
				require.Error(t, err)
				if tt.wantSpecificError != nil {
					require.ErrorIs(t, err, tt.wantSpecificError)
				}
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.want, got)
		})
	}
}

func TestContainerLogging(t *testing.T) {
	t.Parallel()

	logsReader, logsWriter := io.Pipe()
	stdout := stdcopy.NewStdWriter(logsWriter, stdcopy.Stdout)
	stderr := stdcopy.NewStdWriter(logsWriter, stdcopy.Stderr)
	container := &Container{
		logsResp: types.HijackedResponse{
			Reader: bufio.NewReader(logsReader),
		},
		logs: make(chan exec.LogLine),
	}
	container.consumeLogs()

	_, err := io.WriteString(stdout, "hello from stdout\n")
	require.NoError(t, err)

	stdoutLine := <-container.Logs()
	require.NotZero(t, stdoutLine.Time)
	require.Equal(t, exec.LogStreamStdout, stdoutLine.Stream)
	require.Equal(t, "hello from stdout", stdoutLine.Message)
	require.False(t, stdoutLine.IsSentinel)

	_, err = io.WriteString(stderr, "hello from stderr\n")
	require.NoError(t, err)

	stderrLine := <-container.Logs()
	require.NotZero(t, stderrLine.Time)
	require.Equal(t, exec.LogStreamStderr, stderrLine.Stream)
	require.Equal(t, "hello from stderr", stderrLine.Message)
	require.False(t, stderrLine.IsSentinel)

	_, err = stdout.Write(append(logSentinel, "\n"...))
	require.NoError(t, err)

	stdoutSentinel := <-container.Logs()
	require.Zero(t, stdoutSentinel.Time)
	require.Equal(t, exec.LogStreamStdout, stdoutSentinel.Stream)
	require.True(t, stdoutSentinel.IsSentinel)

	_, err = stderr.Write(append(logSentinel, "\n"...))
	require.NoError(t, err)

	stderrSentinel := <-container.Logs()
	require.Zero(t, stderrSentinel.Time)
	require.Equal(t, exec.LogStreamStderr, stderrSentinel.Stream)
	require.True(t, stderrSentinel.IsSentinel)

	// Write log and sentinel to a single line.
	_, err = io.WriteString(stdout, "hello from a single line")
	require.NoError(t, err)
	_, err = stdout.Write(append(logSentinel, "\n"...))
	require.NoError(t, err)

	stdoutLine = <-container.Logs()
	require.NotZero(t, stdoutLine.Time)
	require.Equal(t, exec.LogStreamStdout, stdoutLine.Stream)
	require.Equal(t, "hello from a single line", stdoutLine.Message)
	require.False(t, stdoutLine.IsSentinel)

	stdoutSentinel = <-container.Logs()
	require.Zero(t, stdoutSentinel.Time)
	require.Equal(t, exec.LogStreamStdout, stdoutSentinel.Stream)
	require.True(t, stdoutSentinel.IsSentinel)
}

func TestContainerLoggingNormalRemove(t *testing.T) {
	t.Parallel()

	// Write two messages. The first one will be consumed below, the second "leaks", simulating a
	// log line being written after an invocation, e.g., because the function timed out.
	var buffer bytes.Buffer
	stdout := stdcopy.NewStdWriter(&buffer, stdcopy.Stdout)
	_, err := io.WriteString(stdout, "hello from stdout\nthis message leaks\n")
	require.NoError(t, err)

	container := &Container{
		containerClient: &fakeContainerAPIClient{
			removeFn: func(ctx context.Context, container string, options types.ContainerRemoveOptions) error {
				return nil
			},
		},
		logsResp: types.HijackedResponse{
			Reader: bufio.NewReader(&buffer),
			Conn:   fakeConn{},
		},
		logs:      make(chan exec.LogLine),
		releaseIP: func() {},
	}
	container.consumeLogs()

	// Consume the first message.
	stdoutLine := <-container.Logs()
	require.NotZero(t, stdoutLine.Time)
	require.Equal(t, exec.LogStreamStdout, stdoutLine.Stream)
	require.Equal(t, "hello from stdout", stdoutLine.Message)
	require.False(t, stdoutLine.IsSentinel)

	// Ensure that no error is returned in the "normal" EOF case.
	err = container.Remove(context.Background())
	require.NoError(t, err)
}

func TestContainerLoggingErrorRemove(t *testing.T) {
	t.Parallel()

	logsReader, logsWriter := io.Pipe()
	stdout := stdcopy.NewStdWriter(logsWriter, stdcopy.Stdout)
	var removeCalls int
	var ipReleased bool
	container := &Container{
		containerClient: &fakeContainerAPIClient{
			removeFn: func(ctx context.Context, container string, options types.ContainerRemoveOptions) error {
				removeCalls++
				return nil
			},
		},
		logsResp: types.HijackedResponse{
			Reader: bufio.NewReader(logsReader),
			Conn:   fakeConn{},
		},
		logs:      make(chan exec.LogLine),
		releaseIP: func() { ipReleased = true },
	}
	container.consumeLogs()

	_, err := io.WriteString(stdout, "hello from stdout\n")
	require.NoError(t, err)

	stdoutLine := <-container.Logs()
	require.NotZero(t, stdoutLine.Time)
	require.Equal(t, exec.LogStreamStdout, stdoutLine.Stream)
	require.Equal(t, "hello from stdout", stdoutLine.Message)
	require.False(t, stdoutLine.IsSentinel)

	// Close the pipes.
	logsWriter.CloseWithError(assert.AnError)

	// Ensure that the respective error is returned.
	err = container.Remove(context.Background())
	require.ErrorIs(t, err, assert.AnError)

	// Ensure that the container is still removed.
	require.Equal(t, 1, removeCalls)
	require.True(t, ipReleased)
}

func TestContainerLoggingExceedsLimits(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	stdout := stdcopy.NewStdWriter(&buffer, stdcopy.Stdout)

	// Write 4 lines: A large one, exceeding the limit ...
	exceedingLine := strings.Repeat("a", int(exec.MaxLogLimit)+1)
	_, err := io.WriteString(stdout, exceedingLine+"\n")
	require.NoError(t, err)
	// ... a large one, just within the limit ...
	// We subtract 1 from the limit to make room for the newline.
	largeLine := strings.Repeat("b", int(exec.MaxLogLimit)-1)
	_, err = io.WriteString(stdout, largeLine+"\n")
	require.NoError(t, err)
	// ... a normal one ...
	_, err = io.WriteString(stdout, "hello from stdout\n")
	require.NoError(t, err)
	// ... a sentinel.
	_, err = stdout.Write(append(logSentinel, "\n"...))
	require.NoError(t, err)

	container := &Container{
		containerClient: &fakeContainerAPIClient{
			removeFn: func(ctx context.Context, container string, options types.ContainerRemoveOptions) error {
				return nil
			},
		},
		logsResp: types.HijackedResponse{
			Reader: bufio.NewReader(&buffer),
			Conn:   fakeConn{},
		},
		logs:      make(chan exec.LogLine),
		releaseIP: func() {},
	}
	container.consumeLogs()

	// Consume the first message. It's only read until the limit.
	stdoutLine := <-container.Logs()
	require.Len(t, stdoutLine.Message, int(exec.MaxLogLimit))
	require.Equal(t, exceedingLine[:len(exceedingLine)-1], stdoutLine.Message)

	// The second message is the remainder of the first message.
	stdoutLine2 := <-container.Logs()
	require.Len(t, stdoutLine2.Message, 1)
	require.Equal(t, exceedingLine[len(exceedingLine)-1:], stdoutLine2.Message)

	// The third line is precisely the large line.
	stdoutLine3 := <-container.Logs()
	require.Equal(t, largeLine, stdoutLine3.Message)

	// The fourth line is precisely the "normal" line.
	stdoutLine4 := <-container.Logs()
	require.Equal(t, "hello from stdout", stdoutLine4.Message)

	// The last line is the sentinel and can still be processed correctly.
	stdoutLine5 := <-container.Logs()
	require.True(t, stdoutLine5.IsSentinel)

	// Ensure that no error is returned in the "normal" EOF case.
	err = container.Remove(context.Background())
	require.NoError(t, err)
}

func TestContainerState(t *testing.T) {
	t.Parallel()

	container := &Container{
		containerClient: &fakeContainerAPIClient{
			inspectFn: func(ctx context.Context, container string) (types.ContainerJSON, error) {
				return types.ContainerJSON{
					ContainerJSONBase: &types.ContainerJSONBase{
						State: &types.ContainerState{
							Status:    "dead",
							OOMKilled: true,
						},
					},
				}, nil
			},
		},
	}
	got, err := container.State(context.Background())
	require.NoError(t, err)
	require.Equal(t, exec.ContainerState{
		Status:    "dead",
		OOMKilled: true,
	}, got)
}

func TestContainerStateError(t *testing.T) {
	t.Parallel()

	container := &Container{
		containerClient: &fakeContainerAPIClient{
			inspectFn: func(ctx context.Context, container string) (types.ContainerJSON, error) {
				return types.ContainerJSON{}, assert.AnError
			},
		},
	}
	_, err := container.State(context.Background())
	require.ErrorIs(t, err, assert.AnError)
}

func TestContainerPauseEventuallyUnpauseIfNeeded(t *testing.T) {
	t.Parallel()

	seenPause := make(chan struct{})
	var pauseCalls, unpauseCalls atomic.Int64
	container := &Container{
		containerClient: &fakeContainerAPIClient{
			pauseFn: func(ctx context.Context, container string) error {
				pauseCalls.Add(1)
				seenPause <- struct{}{}
				return nil
			},
			unpauseFn: func(ctx context.Context, container string) error {
				unpauseCalls.Add(1)
				return nil
			},
		},
		pauseStops:     make(chan struct{}),
		pauseResponses: make(chan pauseResponse),
	}

	container.PauseEventually(0 * time.Millisecond)
	<-seenPause // Wait for the pause call to have actually happened.
	require.EqualValues(t, 1, pauseCalls.Load())

	err := container.UnpauseIfNeeded(context.Background())
	require.NoError(t, err)
	// Unpause is then guaranteed to be called on unpause.
	require.EqualValues(t, 1, unpauseCalls.Load())
}

func TestContainerSkipUnpause(t *testing.T) {
	t.Parallel()

	var pauseCalls, unpauseCalls atomic.Int64
	container := &Container{
		containerClient: &fakeContainerAPIClient{
			pauseFn: func(ctx context.Context, container string) error {
				pauseCalls.Add(1)
				return nil
			},
			unpauseFn: func(ctx context.Context, container string) error {
				unpauseCalls.Add(1)
				return nil
			},
		},
		pauseStops:     make(chan struct{}),
		pauseResponses: make(chan pauseResponse),
	}

	start := time.Now()
	pauseGrace := 1 * time.Minute
	container.PauseEventually(pauseGrace)
	// Immediately calling UnpauseIfNeeded should bypass the pause call.
	err := container.UnpauseIfNeeded(context.Background())
	require.NoError(t, err)

	// Assert that the combined time of PauseEventually and UnpauseIfNeeded was less than the pause
	// grace to catch potential regressions with the abortion handling.
	require.Less(t, time.Since(start), pauseGrace)

	require.EqualValues(t, 0, pauseCalls.Load())
	require.EqualValues(t, 0, unpauseCalls.Load())
}

func TestContainerPauseEventuallyError(t *testing.T) {
	t.Parallel()

	seenPause := make(chan struct{})
	var unpauseCalls atomic.Int64
	container := &Container{
		containerClient: &fakeContainerAPIClient{
			pauseFn: func(ctx context.Context, container string) error {
				seenPause <- struct{}{}
				return assert.AnError
			},
			unpauseFn: func(ctx context.Context, container string) error {
				unpauseCalls.Add(1)
				return nil
			},
		},
		pauseStops:     make(chan struct{}),
		pauseResponses: make(chan pauseResponse),
	}

	container.PauseEventually(0 * time.Millisecond)
	<-seenPause

	err := container.UnpauseIfNeeded(context.Background())
	require.ErrorIs(t, err, assert.AnError)

	// Unpause should be skipped.
	require.EqualValues(t, 0, unpauseCalls.Load())
}

func TestContainerUnpauseIfNeededError(t *testing.T) {
	t.Parallel()

	seenPause := make(chan struct{})
	container := &Container{
		containerClient: &fakeContainerAPIClient{
			pauseFn: func(ctx context.Context, container string) error {
				seenPause <- struct{}{}
				return nil
			},
			unpauseFn: func(ctx context.Context, container string) error {
				return assert.AnError
			},
		},
		pauseStops:     make(chan struct{}),
		pauseResponses: make(chan pauseResponse),
	}

	container.PauseEventually(0 * time.Millisecond)
	<-seenPause

	err := container.UnpauseIfNeeded(context.Background())
	require.ErrorIs(t, err, assert.AnError)
}

func TestContainerPauseRetry(t *testing.T) {
	t.Parallel()

	var pauseCalls atomic.Int64
	container := &Container{
		containerClient: &fakeContainerAPIClient{
			pauseFn: func(ctx context.Context, container string) error {
				calls := pauseCalls.Add(1)
				if calls < 3 {
					// Return a retriable error twice.
					return errors.New("container is already paused")
				}
				// Eventually succeed.
				return nil
			},
		},
	}

	require.NoError(t, container.Pause(context.Background()))
	require.EqualValues(t, 3, pauseCalls.Load())
}

func TestContainerPauseRetryEventuallyStops(t *testing.T) {
	t.Parallel()

	var pauseCalls atomic.Int64
	errAlreadyPaused := errors.New("container is already paused")
	container := &Container{
		containerClient: &fakeContainerAPIClient{
			pauseFn: func(ctx context.Context, container string) error {
				pauseCalls.Add(1)
				// Return a retriable error all the time.
				return errAlreadyPaused
			},
		},
	}

	require.ErrorIs(t, container.Pause(context.Background()), errAlreadyPaused)
	require.EqualValues(t, pauseUnpauseRetries+1, pauseCalls.Load())
}

func TestContainerUnpauseRetry(t *testing.T) {
	t.Parallel()

	var unpauseCalls atomic.Int64
	container := &Container{
		containerClient: &fakeContainerAPIClient{
			unpauseFn: func(ctx context.Context, container string) error {
				calls := unpauseCalls.Add(1)
				if calls < 3 {
					// Return a retriable error twice.
					return errors.New("container is not paused")
				}
				// Eventually succeed.
				return nil
			},
		},
	}

	require.NoError(t, container.Unpause(context.Background()))
	require.EqualValues(t, 3, unpauseCalls.Load())
}

func TestContainerUnpauseRetryEventuallyStops(t *testing.T) {
	t.Parallel()

	var unpauseCalls atomic.Int64
	errAlreadyPaused := errors.New("container is not paused")
	container := &Container{
		containerClient: &fakeContainerAPIClient{
			unpauseFn: func(ctx context.Context, container string) error {
				unpauseCalls.Add(1)
				// Return a retriable error all the time.
				return errAlreadyPaused
			},
		},
	}

	require.ErrorIs(t, container.Unpause(context.Background()), errAlreadyPaused)
	require.EqualValues(t, pauseUnpauseRetries+1, unpauseCalls.Load())
}

func TestContainerRemove(t *testing.T) {
	t.Parallel()

	var removeCalls int
	var ipReleased bool
	container := &Container{
		containerClient: &fakeContainerAPIClient{
			removeFn: func(ctx context.Context, container string, options types.ContainerRemoveOptions) error {
				removeCalls++
				return nil
			},
		},
		logsResp: types.HijackedResponse{
			Conn: fakeConn{},
		},
		logs:      make(chan exec.LogLine),
		releaseIP: func() { ipReleased = true },
	}

	err := container.Remove(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, removeCalls)
	require.True(t, ipReleased)
}

func TestContainerRemoveError(t *testing.T) {
	t.Parallel()

	var ipReleased bool
	container := &Container{
		containerClient: &fakeContainerAPIClient{
			removeFn: func(ctx context.Context, container string, options types.ContainerRemoveOptions) error {
				return assert.AnError
			},
		},
		logsResp: types.HijackedResponse{
			Conn: fakeConn{},
		},
		logs:      make(chan exec.LogLine),
		releaseIP: func() { ipReleased = true },
	}

	err := container.Remove(context.Background())
	require.ErrorIs(t, err, assert.AnError)
	require.False(t, ipReleased)
}

// containerCreateRequest captures all parameters passed to a ContainerCreate call.
type containerCreateRequest struct {
	config           *container.Config
	hostConfig       *container.HostConfig
	networkingConfig *network.NetworkingConfig
	platform         *specs.Platform
	containerName    string
}

// fakeContainerAPIClient mocks client.ContainerAPIClient.
type fakeContainerAPIClient struct {
	client.ContainerAPIClient

	createFn  func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *specs.Platform, containerName string) (container.CreateResponse, error)
	startFn   func(ctx context.Context, container string, options types.ContainerStartOptions) error
	inspectFn func(ctx context.Context, container string) (types.ContainerJSON, error)
	attachFn  func(ctx context.Context, container string, options types.ContainerAttachOptions) (types.HijackedResponse, error)
	pauseFn   func(ctx context.Context, container string) error
	unpauseFn func(ctx context.Context, container string) error
	removeFn  func(ctx context.Context, container string, options types.ContainerRemoveOptions) error
}

// ContainerCreate implements client.ContainerAPIClient.
func (f *fakeContainerAPIClient) ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *specs.Platform, containerName string) (container.CreateResponse, error) {
	return f.createFn(ctx, config, hostConfig, networkingConfig, platform, containerName)
}

// ContainerStart implements client.ContainerAPIClient.
func (f *fakeContainerAPIClient) ContainerStart(ctx context.Context, container string, options types.ContainerStartOptions) error {
	return f.startFn(ctx, container, options)
}

// ContainerInspect implements client.ContainerAPIClient.
func (f *fakeContainerAPIClient) ContainerInspect(ctx context.Context, container string) (types.ContainerJSON, error) {
	return f.inspectFn(ctx, container)
}

// ContainerInspect implements client.ContainerAPIClient.
func (f *fakeContainerAPIClient) ContainerAttach(ctx context.Context, container string, options types.ContainerAttachOptions) (types.HijackedResponse, error) {
	return f.attachFn(ctx, container, options)
}

// ContainerPause implements client.ContainerAPIClient.
func (f *fakeContainerAPIClient) ContainerPause(ctx context.Context, container string) error {
	return f.pauseFn(ctx, container)
}

// ContainerUnpause implements client.ContainerAPIClient.
func (f *fakeContainerAPIClient) ContainerUnpause(ctx context.Context, container string) error {
	return f.unpauseFn(ctx, container)
}

// ContainerRemove implements client.ContainerAPIClient.
func (f *fakeContainerAPIClient) ContainerRemove(ctx context.Context, container string, options types.ContainerRemoveOptions) error {
	return f.removeFn(ctx, container, options)
}

// roundTripResponse captures the potential return values of http.RoundTripper.RoundTrip.
type roundTripResponse struct {
	response *http.Response
	err      error
}

// fakeTransport mocks http.RoundTripper.
type fakeTransport struct {
	responses []roundTripResponse
}

// RoundTrip implements http.RoundTripper.
func (f *fakeTransport) RoundTrip(*http.Request) (*http.Response, error) {
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp.response, resp.err
}

// fakeTransportFunc mocks http.RoundTripper in a single function.
type fakeTransportFunc func(*http.Request) (*http.Response, error)

// RoundTrip implements http.RoundTripper.
func (f fakeTransportFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// fakeConn mocks net.Conn.
type fakeConn struct {
	net.Conn
}

func (f fakeConn) Close() error {
	return nil
}

func int64Pointer(i int64) *int64 {
	return &i
}

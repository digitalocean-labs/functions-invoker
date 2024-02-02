package firewall

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/stretchr/testify/require"
)

func TestExecReturnsStdout(t *testing.T) {
	t.Parallel()

	logsReader, logsWriter := io.Pipe()
	stdout := stdcopy.NewStdWriter(logsWriter, stdcopy.Stdout)
	stderr := stdcopy.NewStdWriter(logsWriter, stdcopy.Stderr)
	dockerClient := &fakeContainerAPIClient{
		execCreateFn: func(ctx context.Context, container string, config types.ExecConfig) (types.IDResponse, error) {
			return types.IDResponse{ID: "testid"}, nil
		},
		execAttachFn: func(ctx context.Context, execID string, config types.ExecStartCheck) (types.HijackedResponse, error) {
			return types.HijackedResponse{
				Conn:   &fakeConn{},
				Reader: bufio.NewReader(logsReader),
			}, nil
		},
		execInspectFn: func(ctx context.Context, execID string) (types.ContainerExecInspect, error) {
			return types.ContainerExecInspect{ExitCode: 0}, nil
		},
	}

	want := []byte("Hello from stdout!")
	go func() {
		stdout.Write(want)
		stderr.Write([]byte("you won't see me!"))
		logsWriter.Close()
	}()

	got, err := exec(context.Background(), dockerClient, "testcontainer", []string{}, nil)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestExecWritesStdin(t *testing.T) {
	t.Parallel()

	logsReader, logsWriter := io.Pipe()
	stdout := stdcopy.NewStdWriter(logsWriter, stdcopy.Stdout)
	stderr := stdcopy.NewStdWriter(logsWriter, stdcopy.Stderr)
	conn := &fakeConn{}
	dockerClient := &fakeContainerAPIClient{
		execCreateFn: func(ctx context.Context, container string, config types.ExecConfig) (types.IDResponse, error) {
			return types.IDResponse{ID: "testid"}, nil
		},
		execAttachFn: func(ctx context.Context, execID string, config types.ExecStartCheck) (types.HijackedResponse, error) {
			return types.HijackedResponse{
				Conn:   conn,
				Reader: bufio.NewReader(logsReader),
			}, nil
		},
		execInspectFn: func(ctx context.Context, execID string) (types.ContainerExecInspect, error) {
			return types.ContainerExecInspect{ExitCode: 0}, nil
		},
	}

	wantIn := []byte("Hello from stdin!")
	wantOut := []byte("Hello from stdout!")
	go func() {
		stdout.Write(wantOut)
		stderr.Write([]byte("you won't see me!"))
		logsWriter.Close()
	}()

	got, err := exec(context.Background(), dockerClient, "testcontainer", []string{}, wantIn)
	require.NoError(t, err)
	require.Equal(t, wantOut, got)
	require.Equal(t, wantIn, conn.written.Bytes())
}

func TestExecReturnsStdoutAndStderrInError(t *testing.T) {
	t.Parallel()

	logsReader, logsWriter := io.Pipe()
	stdout := stdcopy.NewStdWriter(logsWriter, stdcopy.Stdout)
	stderr := stdcopy.NewStdWriter(logsWriter, stdcopy.Stderr)
	dockerClient := &fakeContainerAPIClient{
		execCreateFn: func(ctx context.Context, container string, config types.ExecConfig) (types.IDResponse, error) {
			return types.IDResponse{ID: "testid"}, nil
		},
		execAttachFn: func(ctx context.Context, execID string, config types.ExecStartCheck) (types.HijackedResponse, error) {
			return types.HijackedResponse{
				Conn:   &fakeConn{},
				Reader: bufio.NewReader(logsReader),
			}, nil
		},
		execInspectFn: func(ctx context.Context, execID string) (types.ContainerExecInspect, error) {
			return types.ContainerExecInspect{ExitCode: 1}, nil
		},
	}

	wantOut := []byte("Hello from stdout!")
	wantErr := []byte("Hello from stderr!")
	go func() {
		stdout.Write(wantOut)
		stderr.Write(wantErr)
		logsWriter.Close()
	}()

	_, err := exec(context.Background(), dockerClient, "testcontainer", []string{}, nil)
	require.Error(t, err)
	require.ErrorContains(t, err, string(wantOut))
	require.ErrorContains(t, err, string(wantErr))
}

// fakeContainerAPIClient mocks client.ContainerAPIClient.
type fakeContainerAPIClient struct {
	client.ContainerAPIClient

	execCreateFn  func(ctx context.Context, container string, config types.ExecConfig) (types.IDResponse, error)
	execAttachFn  func(ctx context.Context, execID string, config types.ExecStartCheck) (types.HijackedResponse, error)
	execInspectFn func(ctx context.Context, execID string) (types.ContainerExecInspect, error)
}

// ContainerExecCreate implements client.ContainerAPIClient.
func (f *fakeContainerAPIClient) ContainerExecCreate(ctx context.Context, container string, config types.ExecConfig) (types.IDResponse, error) {
	return f.execCreateFn(ctx, container, config)
}

// ContainerExecAttach implements client.ContainerAPIClient.
func (f *fakeContainerAPIClient) ContainerExecAttach(ctx context.Context, execID string, config types.ExecStartCheck) (types.HijackedResponse, error) {
	return f.execAttachFn(ctx, execID, config)
}

// ContainerExecInspect implements client.ContainerAPIClient.
func (f *fakeContainerAPIClient) ContainerExecInspect(ctx context.Context, execID string) (types.ContainerExecInspect, error) {
	return f.execInspectFn(ctx, execID)
}

// fakeConn mocks net.Conn.
type fakeConn struct {
	net.Conn

	written bytes.Buffer
}

func (f *fakeConn) Write(b []byte) (n int, err error) {
	return f.written.Write(b)
}

func (f *fakeConn) Close() error {
	return nil
}

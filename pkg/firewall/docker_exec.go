package firewall

import (
	"bytes"
	"context"
	"fmt"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

var (
	_ IPTables = (*DockerContainerBasedIPTables)(nil)
)

// containerName is the name of a prelaunched container that has access to the host's network
// so that iptables commands can be executed in it.
const containerName = "firewall"

// DockerContainerBasedIPTables implements IPTables with calling the respective commands in a "proxy"
// container that has the respective rights to change iptables.
type DockerContainerBasedIPTables struct {
	DockerClient client.ContainerAPIClient
}

// GetState implements IPTables.
func (d DockerContainerBasedIPTables) GetState(ctx context.Context) ([]byte, error) {
	return exec(ctx, d.DockerClient, containerName, []string{"iptables-save", "--table", "filter"}, nil)
}

// WriteState implements IPTables.
func (d DockerContainerBasedIPTables) WriteState(ctx context.Context, state []byte) error {
	_, err := exec(ctx, d.DockerClient, containerName, []string{"iptables-restore", "--table", "filter"}, state)
	return err
}

// Exec implements IPTables.
func (d DockerContainerBasedIPTables) Exec(ctx context.Context, args []string) error {
	_, err := exec(ctx, d.DockerClient, containerName, append([]string{"iptables"}, args...), nil)
	return err
}

// exec executes the given command in the given container. It returns the stdout if the command was
// successful (returned with exitCode 0). If it wasn't successful, this'll return an error.
// Input can be passed optionally and will be provided to the command via stdin.
func exec(ctx context.Context, cli client.ContainerAPIClient, container string, args []string, input []byte) ([]byte, error) {
	exec, err := cli.ContainerExecCreate(ctx, container, types.ExecConfig{
		AttachStdout: true,
		AttachStderr: true,
		AttachStdin:  len(input) > 0,
		Cmd:          args,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create exec: %w", err)
	}

	// ExecAttach starts the command and attaches the streams.
	resp, err := cli.ContainerExecAttach(ctx, exec.ID, types.ExecStartCheck{})
	if err != nil {
		return nil, fmt.Errorf("failed to attach to exec: %w", err)
	}
	defer resp.Close()

	if len(input) > 0 {
		if _, err := resp.Conn.Write(input); err != nil {
			return nil, fmt.Errorf("failed to write exec stdin: %w", err)
		}
		// Close the writing end of the connection to indicate that STDIN is done.
		if err := resp.CloseWrite(); err != nil {
			return nil, fmt.Errorf("failed to close stdin: %w", err)
		}
	}

	// Consume both stdout and stderr.
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, resp.Reader); err != nil {
		return nil, fmt.Errorf("failed to read exec output: %w", err)
	}

	// Once the output streams close, we can check the exit code.
	inspectResp, err := cli.ContainerExecInspect(ctx, exec.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect exec for status code: %w", err)
	}
	if inspectResp.ExitCode != 0 {
		return nil, fmt.Errorf("failed to execute command, exitCode: %d, stdout: %s, stderr: %s", inspectResp.ExitCode, stdout.String(), stderr.String())
	}
	return stdout.Bytes(), nil
}

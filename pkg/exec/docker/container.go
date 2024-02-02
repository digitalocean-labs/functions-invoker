package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"invoker/pkg/exec"
	"invoker/pkg/logging"
	"invoker/pkg/units"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	dockerunits "github.com/docker/go-units"
	"golang.org/x/sync/errgroup"
)

const (
	// runtimeProxyPort is the port the runtime's HTTP server listens on.
	runtimeProxyPort = "8080"
	// pauseUnpauseRetries specifies how often we retry pause/unpause if it fails due to what seems
	// like inconsistent state. This is an unscientific number. The vast majority of cases seen have
	// resolved with a single retry, but we've seen very occasional retries up to 5 times. So we
	// leave a bit of breathing room.
	pauseUnpauseRetries = 10
	// This is a timeout for container lifecycle commands in general. We've seen container getting
	// stuck, on removes in particular, so this is supposed to unwedge things and inform us through
	// the surfaced errors more explicitly.
	lifecycleHangTimeout = 10 * time.Minute
)

var (
	// logSentinel is the value of a log line that determines that the stream in the context of an
	// invocation has ended.
	logSentinel = []byte("XXX_THE_END_OF_A_WHISK_ACTIVATION_XXX")
)

// Container is a container that can execute a function.
// Its implementation is docker specific to avoid unnecessary layers of abstraction.
type Container struct {
	// id is the ID of the container.
	id string
	// ip is the IP of the container.
	ip string

	// releaseIP releases the IP of the given container.
	releaseIP func()

	// containerClient to manage the lifecycle through Docker.
	containerClient client.ContainerAPIClient

	// http related fields.
	httpClient *http.Client
	runURL     string
	initURL    string

	// log reading related fields.
	logsResp types.HijackedResponse
	logsGrp  errgroup.Group
	// logs contains all of the log lines from the container as they happen. It's expected that this
	// channel is consumed during execution of either `Initialize` or `Run` to not block execution
	// of the function itself.
	logs chan exec.LogLine

	// lifecycle related fields.
	pauseStops     chan struct{}
	pauseResponses chan pauseResponse
}

// pauseResponse is a response from a pause command.
type pauseResponse struct {
	isPaused bool
	err      error
}

// ContainerFactory implements the ContainerFactory interface using docker.
type ContainerFactory struct {
	// Client is the docker client to use for docker requests.
	Client client.ContainerAPIClient
	// Transport is the transport to use when talking to the created containers.
	Transport http.RoundTripper

	// RuntimeManifests allow us to figure out which image to use for which kind.
	RuntimeManifests exec.RuntimeManifests

	// Container configuration.
	// APIHost is the host to pass into the container via env.
	APIHost string
	// DNSServers specifies which DNS servers to use for the container.
	DNSServers []string
	// Network specifies the network used by the container.
	Network string

	ipMux     sync.Mutex
	UsableIPs []string
}

// acquireIP returns an IP from the list of usable IPs and removes it from that list.
func (d *ContainerFactory) acquireIP() (string, error) {
	d.ipMux.Lock()
	defer d.ipMux.Unlock()

	if len(d.UsableIPs) == 0 {
		return "", errors.New("there are no IPs left to acquire")
	}

	ip := d.UsableIPs[0]
	d.UsableIPs = d.UsableIPs[1:]
	return ip, nil
}

// releaseIP returns the given IP to the pool of usable IPs.
func (d *ContainerFactory) releaseIP(ip string) {
	d.ipMux.Lock()
	defer d.ipMux.Unlock()

	d.UsableIPs = append(d.UsableIPs, ip)
}

// CreateContainer implements ContainerClient.
func (d *ContainerFactory) CreateContainer(ctx context.Context, kind string, memory units.ByteSize, name string) (exec.Container, error) {
	contextEvent := logging.ContextEvent(ctx)
	ctx, cancel := context.WithTimeout(ctx, lifecycleHangTimeout)
	defer cancel()

	runtimeManifest, ok := d.RuntimeManifests[kind]
	if !ok {
		return nil, fmt.Errorf("failed to get runtime manifest for kind %q to create container", kind)
	}

	// Create and start the container.
	runStart := time.Now()
	pidsLimit := int64(1024)
	ipAddress, err := d.acquireIP()
	if err != nil {
		return nil, fmt.Errorf("failed to acquire IP address: %w", err)
	}

	resp, err := d.Client.ContainerCreate(ctx,
		&container.Config{
			Image: string(runtimeManifest.Image), // --image $image
			// Bend the runtime's workdir to /tmp so all write of the runtimes are happening in the
			// mounted tmpfs (they all write relative to the workdir).
			WorkingDir: "/tmp", // --workdir /tmp
			// Also bend $HOME to point to /tmp as well so all default cache structures etc. go there
			// as well.
			Env: []string{"HOME=/tmp", "__OW_API_HOST=" + d.APIHost}, // -e HOME=/tmp ...
		},
		&container.HostConfig{
			// The tmpfs' size is shared with the container's memory. Setting it to the same size
			// so that the filesystem reports that number correctly.
			Tmpfs:       map[string]string{"/tmp": fmt.Sprintf("rw,exec,nosuid,nodev,size=%dm", memory/units.Megabyte)}, // --tmpfs /tmp:rw,exec,nosuid,nodev,size=128m
			CapDrop:     []string{"ALL"},                                                                                // --cap-drop=ALL
			LogConfig:   container.LogConfig{Type: "none"},                                                              // --log-driver=json-file
			SecurityOpt: []string{"no-new-privileges"},                                                                  // --security-opt=no-new-privileges
			// Make the container's file-system read only and mount a tmpfs to only allow writes to /tmp.
			ReadonlyRootfs: true,    // --read-only
			Runtime:        "runsc", // --runtime="$runtime"
			Resources: container.Resources{
				CPUShares:  int64(memory / units.Megabyte),                                  // --cpu-shares 256
				Memory:     int64(memory/units.Megabyte) * dockerunits.MB,                   // --memory 256m
				MemorySwap: int64(memory/units.Megabyte) * dockerunits.MB,                   // --memory-swap 256m
				PidsLimit:  &pidsLimit,                                                      // --pids-limit 1024
				Ulimits:    []*dockerunits.Ulimit{{Name: "nofile", Soft: 1024, Hard: 1024}}, // --ulimit=nofile=1024:1024
			},
			DNS: d.DNSServers,
			// Mount a locally available resolv.conf over /etc/resolv.conf to avoid the container
			// from using Docker's internal DNS server, which is not compatible with gVisor.
			Mounts: []mount.Mount{{
				Type:     mount.TypeBind,
				Source:   "/etc/docker/functions.resolv.conf",
				Target:   "/etc/resolv.conf",
				ReadOnly: true,
			}},
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				d.Network: {
					IPAMConfig: &network.EndpointIPAMConfig{
						IPv4Address: ipAddress,
					},
				},
			},
		},
		nil, name)
	if err != nil {
		d.releaseIP(ipAddress)
		return nil, fmt.Errorf("failed to create container: %w", err)
	}

	// Create a partial container already to be able to call Remove on any subsequent errors.
	container := &Container{
		id:              resp.ID,
		ip:              ipAddress,
		releaseIP:       func() { d.releaseIP(ipAddress) },
		containerClient: d.Client,

		httpClient: &http.Client{Transport: d.Transport},
		initURL:    fmt.Sprintf("http://%s/%s", net.JoinHostPort(ipAddress, runtimeProxyPort), "init"),
		runURL:     fmt.Sprintf("http://%s/%s", net.JoinHostPort(ipAddress, runtimeProxyPort), "run"),

		logs: make(chan exec.LogLine),

		pauseStops:     make(chan struct{}),
		pauseResponses: make(chan pauseResponse),
	}

	logsResp, err := d.Client.ContainerAttach(ctx, resp.ID, types.ContainerAttachOptions{Stream: true, Stdout: true, Stderr: true})
	if err != nil {
		return nil, exec.CleanupContainer(ctx, container, fmt.Errorf("failed to attach to containers streams: %w", err))
	}
	container.logsResp = logsResp

	if err := d.Client.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		return nil, exec.CleanupContainer(ctx, container, fmt.Errorf("failed to start container: %w", err))
	}
	runEnd := time.Now()
	runDuration := runEnd.Sub(runStart)
	metricDockerCommandLatencyRun.Observe(runDuration.Seconds())
	contextEvent.Dur("dockerRunTime", runDuration)

	// Setup log consumption into the Logs channel.
	container.consumeLogs()

	// Poll container until it responds.
	pollURL := fmt.Sprintf("http://%s", net.JoinHostPort(container.ip, runtimeProxyPort))
	pollCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := container.pollUntilResponding(pollCtx, pollURL, 50*time.Millisecond); err != nil {
		return nil, exec.CleanupContainer(ctx, container, fmt.Errorf("failed poll for a valid container response: %w", err))
	}
	pollEnd := time.Now()
	pollDuration := pollEnd.Sub(runEnd)
	contextEvent.Dur("pollTime", pollDuration)
	metricContainerPollLatency.Observe(pollDuration.Seconds())

	return container, nil
}

// ID implements Container.
func (c *Container) ID() string {
	return c.id
}

// IP implements Container.
func (c *Container) IP() string {
	return c.ip
}

// consumeLogs consumes the response from the attach operation and splits the stream into stdout
// and stderr, to then consume them into the Logs channel.
//
// Any errors in doing this would not immediately surface but instead be punted into the logsGrp.
// The respective error will then surface in the Remove call, where it can be logged as well.
// We can potentially be more graceful with this, but given that failure to read logs causes the
// container to have to be deleted anyway, we might be just fine with that flow too.
func (c *Container) consumeLogs() {
	stdoutread, stdoutwrite := io.Pipe()
	stderrread, stderrwrite := io.Pipe()
	c.logsGrp.Go(func() error {
		// Close all the pipes once the demultiplexer exits either normally or with an error.
		// This will subsequently cause the consumeStream calls below to exit as well.
		defer stdoutread.Close()
		defer stdoutwrite.Close()
		defer stderrread.Close()
		defer stderrwrite.Close()

		_, err := stdcopy.StdCopy(stdoutwrite, stderrwrite, c.logsResp.Reader)
		if err != nil {
			return fmt.Errorf("failed to demultiplex stdio stream from container: %w", err)
		}
		return nil
	})
	c.logsGrp.Go(func() error { return consumeStream(exec.LogStreamStdout, stdoutread, c.logs) })
	c.logsGrp.Go(func() error { return consumeStream(exec.LogStreamStderr, stderrread, c.logs) })
}

// consumeStream consumes the given reader into the given channel, line-by-line. The produced
// LogLines will be attributed to the given stream (either stdout or stderr).
func consumeStream(stream string, reader io.Reader, into chan exec.LogLine) error {
	buf := bufio.NewReaderSize(reader, int(exec.MaxLogLimit))
	for {
		// ReadLine is called out as a low-level primitive, but that's exactly what we want here.
		// We it to return partial logs and never exceed the buffer size. ReadBytes and ReadString
		// are not applicable as they both apply no limitations to the returned line. A Scanner
		// makes this unnecessarily cumbersome as the default behavior is prone to a hang, see
		// https://github.com/golang/go/issues/35474.
		log, _, err := buf.ReadLine()
		if err != nil {
			// ErrClosedPipe and EOF has to be ignored here, as it's part of the usual lifecycle
			// of the io.Pipes used herein.
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
				return nil
			}
			return fmt.Errorf("failed to read logs: %w", err)
		}

		if trimmed := bytes.TrimSuffix(log, logSentinel); len(trimmed) < len(log) {
			// If the line ends with the log sentinel, check if there has been more than just the
			// sentinel on this log line. If so, emit that prefix as a regular line.
			if len(trimmed) > 0 {
				into <- exec.LogLine{Stream: stream, Time: time.Now(), Message: string(trimmed)}
			}
			into <- exec.LogLine{Stream: stream, IsSentinel: true}
			continue
		}
		into <- exec.LogLine{Stream: stream, Time: time.Now(), Message: string(log)}
	}
}

// Logs returns a channel over all loglines of the container. The lines are written to the channel
// as they happen and its expected they are consumed as quickly as possible.
func (c *Container) Logs() <-chan exec.LogLine {
	return c.logs
}

// pollUntilResponding polls the given URL in the given interval until a successful response is
// received from the container.
func (c *Container) pollUntilResponding(ctx context.Context, pollURL string, pollInterval time.Duration) error {
	pollTicker := time.NewTicker(pollInterval)
	defer pollTicker.Stop()

	var tries int
	var lastErr error
	for {
		tries++

		if _, err := c.request(ctx, pollURL, nil); err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				// Abort retrying once the context has gotten canceled or timed out.
				if lastErr == nil {
					// If lastErr isn't set we've got aborted on the first try.
					return fmt.Errorf("failed to wait for container to respond on first try: %w", err)
				}
				return fmt.Errorf("failed to wait for container to respond in %d tries: last error: %w, abort error: %w", tries, lastErr, err)
			}

			// Store the last error that wasn't a context error to be able to reason about the underlying issue.
			lastErr = fmt.Errorf("failed to poll at %s: %w", time.Now(), err)
			select {
			case <-pollTicker.C:
				// Retry.
				continue
			case <-ctx.Done():
				// Abort retrying once the context has gotten canceled or timed out.
				return fmt.Errorf("failed to wait for container to respond in %d tries: last error: %w, abort error: %w", tries, lastErr, ctx.Err())
			}
		}
		// Reaching here means we haven't seen an error, all is good.
		return nil
	}
}

// Initialize initializes the container. Called only once.
//
// A response is tried to return even if an error is returned. It might contain the allowed bytes
// from a response that exceeded the limit for example.
func (c *Container) Initialize(ctx context.Context, payload exec.ContainerInitPayload) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal init payload: %w", err)
	}

	resp, err := c.request(ctx, c.initURL, body)
	if err != nil {
		return resp, fmt.Errorf("failed to initialize container: %w", err)
	}

	return resp, nil
}

// Run runs the function in the container. Called multiple times.
//
// A response is tried to return even if an error is returned. It might contain the allowed bytes
// from a response that exceeded the limit for example.
func (c *Container) Run(ctx context.Context, payload exec.ContainerRunPayload) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal run payload: %w", err)
	}

	resp, err := c.request(ctx, c.runURL, body)
	if err != nil {
		return resp, fmt.Errorf("failed to run on container: %w", err)
	}

	return resp, nil
}

// request sends an HTTP request to the container and returns the entire body, if possible.
// We try to return body bytes in all cases where we receive a response.
func (c *Container) request(ctx context.Context, url string, payload []byte) ([]byte, error) {
	var content io.Reader
	if payload != nil {
		content = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, content)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	var body []byte
	if resp.ContentLength == -1 {
		// If the Content-Length is not set, we don't want to read the response at all and instead
		// throw it away completely to free the connection.
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			return body, fmt.Errorf("failed to discard body of unknown content-length response: %w", err)
		}
	} else {
		var bytesToRead = resp.ContentLength
		if bytesToRead > int64(exec.ResponseLimit) {
			bytesToRead = int64(exec.ResponseLimit)
		}
		body = make([]byte, bytesToRead)
		if _, err := io.ReadFull(resp.Body, body); err != nil {
			return body, fmt.Errorf("failed to read HTTP body: %w", err)
		}
		if bytesToRead < resp.ContentLength {
			// Since the operation above didn't read the entire response, we throw the rest
			// away.
			if _, err := io.Copy(io.Discard, resp.Body); err != nil {
				return body, fmt.Errorf("failed to discard body of overflow response: %w", err)
			}
			return body, exec.ResponseLimitExceededError{ContentLength: units.ByteSize(resp.ContentLength)}
		}
	}

	if resp.StatusCode != http.StatusOK {
		return body, fmt.Errorf("unexpected HTTP response: status: %d", resp.StatusCode)
	}
	return body, nil
}

// State returns the state of the current container. This can be used to determine if it crashed
// or ran out of memory for example.
func (c *Container) State(ctx context.Context) (exec.ContainerState, error) {
	ctx, cancel := context.WithTimeout(ctx, lifecycleHangTimeout)
	defer cancel()
	resp, err := c.containerClient.ContainerInspect(ctx, c.id)
	if err != nil {
		return exec.ContainerState{}, fmt.Errorf("failed to inspect container to detect OOM: %w", err)
	}
	if resp.ContainerJSONBase == nil || resp.ContainerJSONBase.State == nil {
		return exec.ContainerState{}, nil
	}
	return exec.ContainerState{
		Status:     resp.State.Status,
		Running:    resp.State.Running,
		Paused:     resp.State.Paused,
		OOMKilled:  resp.State.OOMKilled,
		Dead:       resp.State.Dead,
		ExitCode:   resp.State.ExitCode,
		Error:      resp.State.Error,
		StartedAt:  resp.State.StartedAt,
		FinishedAt: resp.State.FinishedAt,
	}, nil
}

// Pause pauses the container.
func (c *Container) Pause(ctx context.Context) error {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, lifecycleHangTimeout)
	defer cancel()

	var retries int
	for {
		if err := c.containerClient.ContainerPause(ctx, c.id); err != nil {
			if strings.Contains(err.Error(), "is already paused") {
				// Retry if docker claims that the container is already paused.
				// This can happen if pause/unpause is running in tight succession.
				if retries == pauseUnpauseRetries {
					return fmt.Errorf("failed to pause container after retrying %d times: %w", retries, err)
				}
				retries++
				continue
			}
			return fmt.Errorf("failed to pause container: %w", err)
		}
		metricDockerCommandLatencyPause.Observe(time.Since(start).Seconds())
		return nil
	}
}

// Unpause unpauses the container.
func (c *Container) Unpause(ctx context.Context) error {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, lifecycleHangTimeout)
	defer cancel()

	var retries int
	for {
		if err := c.containerClient.ContainerUnpause(ctx, c.id); err != nil {
			if strings.Contains(err.Error(), "not paused") {
				// Retry if docker claims that the container isn't actually paused.
				// This can happen if pause/unpause is running in tight succession.
				if retries == pauseUnpauseRetries {
					return fmt.Errorf("failed to unpause container after retrying %d times: %w", retries, err)
				}
				retries++
				continue
			}
			return fmt.Errorf("failed to unpause container: %w", err)
		}
		metricDockerCommandLatencyUnpause.Observe(time.Since(start).Seconds())
		return nil
	}
}

// PauseEventually schedules a pause operation to be done eventually after the given grace period
// has passed. The grace period will be aborted by UnpauseIfNeeded.
func (c *Container) PauseEventually(pauseGrace time.Duration) {
	go func() {
		timer := time.NewTimer(pauseGrace)
		defer timer.Stop()

		select {
		case <-timer.C:
			err := c.Pause(context.Background())

			<-c.pauseStops // Force reading from here. Guarantees reusability of the channel.
			c.pauseResponses <- pauseResponse{isPaused: true, err: err}
		case <-c.pauseStops:
			c.pauseResponses <- pauseResponse{isPaused: false}
		}
	}()
}

// UnpauseIfNeeded potentially aborts a scheduled pause request from PauseEventually and unpauses
// the container immediately, if necessary.
func (c *Container) UnpauseIfNeeded(ctx context.Context) error {
	pauseStart := time.Now()
	c.pauseStops <- struct{}{}
	resp := <-c.pauseResponses

	if resp.err != nil {
		// Return an error that happened during PauseEventually to be able to react to it.
		return fmt.Errorf("failed to pause container asynchronously: %w", resp.err)
	}
	if !resp.isPaused {
		return nil
	}

	unpauseStart := time.Now()
	if err := c.Unpause(ctx); err != nil {
		return fmt.Errorf("failed to unpause container: %w", err)
	}

	logging.ContextEvent(ctx).
		// pauseRestTime reports the amount of time this given UnpauseIfNeeded operation had to wait
		// for a pause call to finish, that was already in flight.
		TimeDiff("pauseRestTime", unpauseStart, pauseStart).
		TimeDiff("unpauseTime", time.Now(), unpauseStart)
	return nil
}

// Remove removes the container and cleans up all resources.
func (c *Container) Remove(ctx context.Context) error {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, lifecycleHangTimeout)
	defer cancel()
	if err := c.containerClient.ContainerRemove(ctx, c.id, types.ContainerRemoveOptions{Force: true}); err != nil {
		// Return early. Failing this means we've leaked stuff anyway.
		return fmt.Errorf("failed to remove container: %w", err)
	}
	metricDockerCommandLatencyRemove.Observe(time.Since(start).Seconds())

	// Once the container was successfully deleted, we can free its IP.
	c.releaseIP()

	if c.logsResp.Conn != nil {
		drainedCh := make(chan struct{})
		go func() {
			defer close(drainedCh)

			// Drain the logs channel in case some lines were written outside the invocation. This
			// might happen for timeouts for example.
			// Reading from this channel allows the consumeStream calls above to eventually finish.
			//
			//nolint:revive // Empty block is okay here as we're just draining the channel.
			for range c.logs {
			}
		}()

		if err := c.logsResp.Conn.Close(); err != nil {
			// Return early as the Wait below would likely hang and cause us to never see the error at all.
			return fmt.Errorf("failed to close attach connection: %w", err)
		}

		// Wait for the log consumption goroutines to shut down.
		waitErr := c.logsGrp.Wait()

		// We know all goroutines have finished now, so close the log draining routines as well to
		// avoid it to leak.
		close(c.logs)
		<-drainedCh

		if waitErr != nil && !errors.Is(waitErr, net.ErrClosed) {
			return fmt.Errorf("failed to wait for log consumption to finish: %w", waitErr)
		}
	}

	return nil
}

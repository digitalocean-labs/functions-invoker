package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/docker/docker/client"
	"github.com/kelseyhightower/envconfig"
	"github.com/montanaflynn/stats"
	"golang.org/x/sync/errgroup"

	"invoker/pkg/exec"
	"invoker/pkg/exec/docker"
	"invoker/pkg/units"
)

func main() {
	// Load the runtime manifest from the invoker's environment.
	var config struct {
		RuntimesManifest exec.RuntimeManifests `required:"true" split_words:"true"`
	}
	if err := envconfig.Process("", &config); err != nil {
		panic(err)
	}

	// Parse the relevant flags.
	nFlag := flag.Int("n", 20, "number of pause and unpause operations to do per container")
	threadsFlag := flag.Int("threads", 1, "number of containers to create in parallel")
	runtimeFlag := flag.String("runtime", "nodejs:default", "the runtime to launch")
	flag.Parse()

	N := *nFlag
	parallelism := *threadsFlag

	// Initialize the docker client and its corresponding container factory.
	dockerClient, err := client.NewClientWithOpts(client.WithAPIVersionNegotiation())
	if err != nil {
		panic(err)
	}

	// This is supposed to run on an "empty" invoker, that has no load going on.
	// As such, we drop the first 10 IPs to be somewhat sure that we're not conflicting
	// with containers from the invoker itself.
	var usableIPs []string
	for i := 12; i < 255; i++ {
		usableIPs = append(usableIPs, fmt.Sprintf("172.18.0.%d", i))
	}
	factory := &docker.ContainerFactory{
		Client:           dockerClient,
		Transport:        http.DefaultTransport,
		RuntimeManifests: config.RuntimesManifest,
		Network:          "functions",
		UsableIPs:        usableIPs,
	}

	fmt.Printf("N       = %d\n", N)
	fmt.Printf("Threads = %d\n", parallelism)

	var (
		pauseOps     = int64(-1)
		pauseTimes   = make([]float64, N*parallelism)
		unpauseOps   = int64(-1)
		unpauseTimes = make([]float64, N*parallelism)
	)

	var grp errgroup.Group
	for i := 0; i < parallelism; i++ {
		grp.Go(func() error {
			c, err := factory.CreateContainer(context.Background(), *runtimeFlag, 256*units.Megabyte, "")
			if err != nil {
				return fmt.Errorf("failed to create container: %w", err)
			}
			defer func() {
				if err := c.Remove(context.Background()); err != nil {
					fmt.Printf("Failed to remove container %q: %v\n", c.ID(), err)
				}
			}()

			for i := 0; i < N; i++ {
				pauseIndex := atomic.AddInt64(&pauseOps, 1)
				pauseStart := time.Now()
				if err := c.Pause(context.Background()); err != nil {
					return fmt.Errorf("failed to pause container: %w", err)
				}
				pauseTimes[pauseIndex] = float64(time.Since(pauseStart) / time.Millisecond)

				unpauseIndex := atomic.AddInt64(&unpauseOps, 1)
				unpauseStart := time.Now()
				if err := c.Unpause(context.Background()); err != nil {
					return fmt.Errorf("failed to unpause container: %w", err)
				}
				unpauseTimes[unpauseIndex] = float64(time.Since(unpauseStart) / time.Millisecond)
			}
			return nil
		})
	}

	if err := grp.Wait(); err != nil {
		panic(err)
	}

	fmt.Println("=== RESULTS ===")
	fmt.Println("--- PAUSE ---")
	fmt.Printf("Min   = %.0f\n", must(stats.Min(pauseTimes)))
	fmt.Printf("Max   = %.0f\n", must(stats.Max(pauseTimes)))
	fmt.Printf("Mean  = %.0f\n", must(stats.Mean(pauseTimes)))
	fmt.Printf("p95   = %.0f\n", must(stats.Percentile(pauseTimes, 95)))
	fmt.Printf("p99   = %.0f\n", must(stats.Percentile(pauseTimes, 99)))
	fmt.Println("--- UNPAUSE ---")
	fmt.Printf("Min   = %.0f\n", must(stats.Min(unpauseTimes)))
	fmt.Printf("Max   = %.0f\n", must(stats.Max(unpauseTimes)))
	fmt.Printf("Mean  = %.0f\n", must(stats.Mean(unpauseTimes)))
	fmt.Printf("p95   = %.0f\n", must(stats.Percentile(unpauseTimes, 95)))
	fmt.Printf("p99   = %.0f\n", must(stats.Percentile(unpauseTimes, 99)))
}

func must(res float64, err error) float64 {
	if err != nil {
		panic(err)
	}
	return res
}

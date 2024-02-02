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
	nFlag := flag.Int("n", 20, "number of containers to create in total")
	threadsFlag := flag.Int("threads", 1, "number of containers to create in parallel")
	runtimeFlag := flag.String("runtime", "nodejs:default", "the runtime to launch")
	flag.Parse()

	N := int64(*nFlag)
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

	done := int64(-1)
	timeUntilResponding := make([]float64, N)
	var grp errgroup.Group
	for i := 0; i < parallelism; i++ {
		grp.Go(func() error {
			for {
				j := atomic.AddInt64(&done, 1)
				if j >= N {
					return nil
				}

				start := time.Now()
				c, err := factory.CreateContainer(context.Background(), *runtimeFlag, 256*units.Megabyte, "")
				if err != nil {
					return fmt.Errorf("failed to create container: %w", err)
				}
				timeUntilResponding[j] = float64(time.Since(start) / time.Millisecond)

				if err := c.Remove(context.Background()); err != nil {
					return fmt.Errorf("failed to remove container: %w", err)
				}
			}
		})
	}

	if err := grp.Wait(); err != nil {
		panic(err)
	}

	fmt.Println("=== RESULTS ===")
	fmt.Printf("Min   = %.0f\n", must(stats.Min(timeUntilResponding)))
	fmt.Printf("Max   = %.0f\n", must(stats.Max(timeUntilResponding)))
	fmt.Printf("Mean  = %.0f\n", must(stats.Mean(timeUntilResponding)))
	fmt.Printf("p95   = %.0f\n", must(stats.Percentile(timeUntilResponding, 95)))
	fmt.Printf("p99   = %.0f\n", must(stats.Percentile(timeUntilResponding, 99)))
}

func must(res float64, err error) float64 {
	if err != nil {
		panic(err)
	}
	return res
}

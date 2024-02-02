package docker

import (
	"invoker/pkg/metrics"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	metricDockerCommandLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "histogram_invoker_docker_finish_seconds",
		Buckets: metrics.TimeBuckets,
	}, []string{"cmd"})
	metricDockerCommandLatencyRun     = metricDockerCommandLatency.With(prometheus.Labels{"cmd": "run"})
	metricDockerCommandLatencyPause   = metricDockerCommandLatency.With(prometheus.Labels{"cmd": "pause"})
	metricDockerCommandLatencyUnpause = metricDockerCommandLatency.With(prometheus.Labels{"cmd": "unpause"})
	metricDockerCommandLatencyRemove  = metricDockerCommandLatency.With(prometheus.Labels{"cmd": "rm"})

	metricContainerPollLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name: "histogram_invoker_containerPoll_finish_seconds",
		// Has a timeout of 30s, not 10s.
		Buckets: []float64{0.005, 0.01, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 20, 30},
	})
)

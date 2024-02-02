package runner

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"invoker/pkg/metrics"
)

var (
	// The default buckets are very granular sub-second but only reach up to 10s. We want much longer running functions
	// than that but don't care as much about the sub-second granularity.
	initAndRunBuckets = []float64{0.005, 0.01, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 240, 480, 960, 1920}

	metricActivations = promauto.NewCounter(prometheus.CounterOpts{
		Name: "counter_invoker_activation_start_total",
	})

	metricInitLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "histogram_invoker_activationInit_finish_seconds",
		Buckets: initAndRunBuckets,
	})
	metricRunLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "histogram_invoker_activationRun_finish_seconds",
		Buckets: initAndRunBuckets,
	})
	metricLogCollectionLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "histogram_invoker_collectLogs_finish_seconds",
		Buckets: metrics.TimeBuckets,
	})

	metricResultSize = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "histogram_activations_resultSize_counter_bytes",
		Buckets: metrics.DataBuckets,
	})
	metricLogSize = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "histogram_activations_logsSize_counter_bytes",
		Buckets: metrics.DataBuckets,
	})

	metricStatus = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "counter_activations_status_counter_total",
	}, []string{"status"})
	metricStatusSuccess          = metricStatus.With(prometheus.Labels{"status": "success"})
	metricStatusApplicationError = metricStatus.With(prometheus.Labels{"status": "application_error"})
	metricStatusDeveloperError   = metricStatus.With(prometheus.Labels{"status": "developer_error"})
	metricStatusSystemError      = metricStatus.With(prometheus.Labels{"status": "system_error"})
)

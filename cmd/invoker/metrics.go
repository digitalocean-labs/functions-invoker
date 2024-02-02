package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"invoker/pkg/metrics"
)

var (
	metricKafkaDelay = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "histogram_kafka_topic_start_seconds",
		Buckets: metrics.TimeBuckets,
	}, []string{"topic"})
)

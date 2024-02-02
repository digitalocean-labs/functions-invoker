package couchdb

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"invoker/pkg/metrics"
)

var (
	metricGetDocument = promauto.NewCounter(prometheus.CounterOpts{
		Name: "counter_database_getDocument_start_total",
	})
	metricGetDocumentFailures = promauto.NewCounter(prometheus.CounterOpts{
		Name: "counter_database_getDocument_error_total",
	})
	metricGetDocumentLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "histogram_database_getDocument_finish_seconds",
		Buckets: metrics.TimeBuckets,
	})

	metricGetAttachment = promauto.NewCounter(prometheus.CounterOpts{
		Name: "counter_database_getDocumentAttachment_start_total",
	})
	metricGetAttachmentFailures = promauto.NewCounter(prometheus.CounterOpts{
		Name: "counter_database_getDocumentAttachment_error_total",
	})
	metricGetAttachmentLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "histogram_database_getDocumentAttachment_finish_seconds",
		Buckets: metrics.TimeBuckets,
	})

	metricSaveDocumentBulk = promauto.NewCounter(prometheus.CounterOpts{
		Name: "counter_database_saveDocumentBulk_start_total",
	})
	metricSaveDocumentBulkFailures = promauto.NewCounter(prometheus.CounterOpts{
		Name: "counter_database_saveDocumentBulk_error_total",
	})
	metricSaveDocumentBulkLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "histogram_database_saveDocumentBulk_finish_seconds",
		Buckets: metrics.TimeBuckets,
	})
	metricBatchSize = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "histogram_database_batchSize_counter",
		Buckets: metrics.DefaultBuckets,
	})
)

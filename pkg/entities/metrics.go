package entities

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	metricCacheHit = promauto.NewCounter(prometheus.CounterOpts{
		Name: "counter_database_cacheHit_counter_total",
	})
	metricCacheMiss = promauto.NewCounter(prometheus.CounterOpts{
		Name: "counter_database_cacheMiss_counter_total",
	})
)

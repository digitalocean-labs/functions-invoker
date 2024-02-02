package exec

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	metricContainerObtained = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "counter_invoker_containerStart_counter_total",
	}, []string{"containerState"})

	metricContainerObtainedCold    = metricContainerObtained.With(prometheus.Labels{"containerState": "cold"})
	metricContainerObtainedPrewarm = metricContainerObtained.With(prometheus.Labels{"containerState": "prewarm"})
	metricContainerObtainedWarm    = metricContainerObtained.With(prometheus.Labels{"containerState": "warm"})
)

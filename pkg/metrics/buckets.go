package metrics

var (
	// DefaultBuckets are the default prometheus buckets as defined by Kamon.
	// See: https://github.com/kamon-io/kamon-prometheus/blob/31c03650aaad296df9bd382550d70eee648949cd/src/main/resources/reference.conf#L14-L24
	DefaultBuckets = []float64{10, 30, 100, 300, 1000, 3000, 10000, 30000, 100000}
	// TimeBuckets are the default prometheus buckets for second-based metrics as defined by Kamon.
	// See: https://github.com/kamon-io/kamon-prometheus/blob/31c03650aaad296df9bd382550d70eee648949cd/src/main/resources/reference.conf#L26-L41
	TimeBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10}
	// DataBuckets are the default prometheus buckets for byte-based metrics as defined by Kamon.
	// See: https://github.com/kamon-io/kamon-prometheus/blob/31c03650aaad296df9bd382550d70eee648949cd/src/main/resources/reference.conf#L43-L52
	DataBuckets = []float64{512, 1024, 2048, 4096, 16384, 65536, 524288, 1048576}
)

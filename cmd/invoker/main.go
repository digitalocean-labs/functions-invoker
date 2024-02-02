package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/http/pprof"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/docker/docker/client"
	"github.com/go-logr/zerologr"
	"github.com/kelseyhightower/envconfig"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"invoker/pkg/clock"
	"invoker/pkg/couchdb"
	"invoker/pkg/encryption"
	"invoker/pkg/entities"
	"invoker/pkg/exec"
	"invoker/pkg/exec/docker"
	"invoker/pkg/firewall"
	invokerhttp "invoker/pkg/http"
	invokerkafka "invoker/pkg/kafka"
	"invoker/pkg/logging"
	"invoker/pkg/protocol"
	"invoker/pkg/runner"
	invokertls "invoker/pkg/tls"
	"invoker/pkg/units"
)

type config struct {
	// ID is the instance ID of the invoker.
	ID int `required:"true"`
	// LogLevel defines the loglevel for all loggers.
	LogLevel logging.Level `required:"true" split_words:"true"`
	// Couch contains the configuration for connecting to CouchDB.
	Couch couchConfig `required:"true" split_words:"true"`
	// KafkaHosts is the hosts configuration for the Kafka clients.
	KafkaHosts string `required:"true" split_words:"true"`
	// UserMemory is the amount of schedulable memory for the given invoker.
	UserMemory units.ByteSize `required:"true" split_words:"true"`
	// MinimumFunctionMemory is the minimum amount of memory that can be specified for running a
	// function.
	MinimumFunctionMemory units.ByteSize `required:"true" split_words:"true"`
	// EncryptionKeys are the keys used to decrypt parameters from the database.
	EncryptionKeys encryption.Keys `required:"true" split_words:"true"`
	// RuntimesManifest are metadata for each supported runtime.
	RuntimesManifest exec.RuntimeManifests `required:"true" split_words:"true"`
	// Containers specifies additional configuration to be passed down to the individual containers.
	Containers containerConfig `require:"true" split_words:"true"`
}

// couchConfig contains the configuration for connecting to CouchDB.
type couchConfig struct {
	// URL is the URL (including protocol and port) where CouchDB is hosted.
	URL string `required:"true" split_words:"true"`
	// Username is the username for the CouchDB.
	Username string `required:"true" split_words:"true"`
	// Password is the password for the CouchDB.
	Password string `required:"true" split_words:"true"`
}

type containerConfig struct {
	// APIHost is the host to pass into the container via env.
	APIHost string `require:"true" split_words:"true"`
	// DNSServers specifies which DNS servers to use for the container.
	DNSServers []string `require:"true" split_words:"true"`
	// Network specifies the network used by the container.
	Network string `require:"true" split_words:"true"`
	// Subnet specifies the subnet in which the container IPs exist.
	Subnet string `require:"true" split_words:"true"`
	// BridgeName is the name of the bridge used for container networking.
	BridgeName string `require:"true" split_words:"true"`
}

func main() {
	// This resembles the time format of our existing components.
	zerolog.TimeFieldFormat = "2006-01-02T15:04:05.999Z07:00"
	zerolog.TimestampFieldName = "@timestamp"
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()

	// Ensure client-go/informer errors are printed in the correct format.
	klog.SetLogger(zerologr.New(&logger))

	// Load the config.
	var c config
	if err := envconfig.Process("", &c); err != nil {
		logger.Fatal().Err(err).Msg("failed to process config")
	}
	logger.Info().Interface("config", c).Msg("config processed successfully")

	// Set the log level.
	zerolog.SetGlobalLevel(zerolog.Level(c.LogLevel))

	// Setup a TLS config that's compatible with our in-cluster TLS certificates.
	tlsConfig, err := invokertls.GetClusterTLSConfig()
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to load cluster TLS config")
	}
	tlsEnabledTransport := http.DefaultTransport.(*http.Transport).Clone() //nolint:forcetypeassert
	tlsEnabledTransport.TLSClientConfig = tlsConfig

	// Initialize the CouchDB client.
	couchURL, err := url.Parse(c.Couch.URL)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to parse CouchDB URL")
	}
	couchDBClient := &couchdb.Client{
		ClientFactory: func() *http.Client {
			// Recreate a new transport every time to force new connections to be created.
			transport := http.DefaultTransport.(*http.Transport).Clone() //nolint:forcetypeassert
			transport.TLSClientConfig = tlsConfig
			return &http.Client{Transport: transport}
		},
		Base:     couchURL,
		Username: c.Couch.Username,
		Password: c.Couch.Password,
	}

	functionReader, err := entities.NewCachedFunctionReader(couchDBClient, 128)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to create cached function reader")
	}
	activationWriter := couchdb.NewActivationBatcher(logger, couchDBClient, runtime.NumCPU())

	// Initialize Kafka consumer and writer.
	topic := fmt.Sprintf("invoker%d", c.ID)
	if err := invokerkafka.CreateInvokerTopic(c.KafkaHosts, tlsConfig, topic); err != nil {
		logger.Fatal().Err(err).Msg("failed to create topic")
	}
	kafkaReader := invokerkafka.NewKafkaReader(c.KafkaHosts, topic, tlsConfig)
	kafkaWriter := invokerkafka.NewKafkaWriter(c.KafkaHosts, tlsConfig)
	kafkaClient := invokerkafka.NewKafkaClient(c.KafkaHosts, tlsConfig)

	// Initialize the docker client and its corresponding container factory.
	dockerClient, err := client.NewClientWithOpts(client.WithAPIVersionNegotiation())
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to create docker client")
	}

	// Initialize a dynamic kubernetes client.
	kubecfg, err := rest.InClusterConfig()
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to load Kubernetes config")
	}
	kube, err := kubernetes.NewForConfig(kubecfg)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to create Kubernetes client")
	}
	coreInformerFactory := informers.NewSharedInformerFactory(kube, 1*time.Hour)

	nodeInformer := coreInformerFactory.Core().V1().Nodes()
	serviceInformer := coreInformerFactory.Core().V1().Services()
	endpointsInformer := coreInformerFactory.Core().V1().Endpoints()

	// Buffer one request to coalesce reconciles while a reconcile is already in-flight.
	firewallPoke := make(chan struct{}, 1)
	handler := firewall.FilteredEventHandler(func() {
		select {
		case firewallPoke <- struct{}{}:
		default:
			// If the write would block, we can safely move on as there's already a poke in the
			// channel to cause a subsequent reconcile.
		}
	})
	if _, err := nodeInformer.Informer().AddEventHandler(handler); err != nil {
		logger.Fatal().Err(err).Msg("failed to register event handler to node informer")
	}
	if _, err := serviceInformer.Informer().AddEventHandler(handler); err != nil {
		logger.Fatal().Err(err).Msg("failed to register event handler to service informer")
	}
	if _, err := endpointsInformer.Informer().AddEventHandler(handler); err != nil {
		logger.Fatal().Err(err).Msg("failed to register event handler to endpoints informer")
	}

	firewallReconciler := &firewall.Reconciler{
		Logger: logger,

		ContainerSubnet: c.Containers.Subnet,
		ContainerBridge: c.Containers.BridgeName,

		IPTables:        firewall.DockerContainerBasedIPTables{DockerClient: dockerClient},
		NodeLister:      nodeInformer.Lister(),
		ServiceLister:   serviceInformer.Lister(),
		EndpointsLister: endpointsInformer.Lister(),
	}

	// Generate usable IPs in the functions network to consume. We start at 2 to avoid using
	// the gateway IP and stop at 255, which should be plenty.
	var usableIPs []string
	for i := 2; i < 255; i++ {
		usableIPs = append(usableIPs, fmt.Sprintf("172.18.0.%d", i))
	}
	containerPool := exec.NewContainerPool(logger, c.UserMemory, c.RuntimesManifest, &docker.ContainerFactory{
		Client:    dockerClient,
		Transport: http.DefaultTransport,

		RuntimeManifests: c.RuntimesManifest,
		APIHost:          c.Containers.APIHost,
		DNSServers:       c.Containers.DNSServers,
		Network:          c.Containers.Network,

		UsableIPs: usableIPs,
	}, firewallReconciler, 15*time.Minute)

	innerRunner := &runner.Runner{
		Clock:            clock.SystemClock{},
		Pool:             containerPool,
		FunctionReader:   functionReader,
		ActivationWriter: activationWriter,
	}

	decryptor, err := encryption.NewDecryptor(c.EncryptionKeys)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to create decryptor")
	}
	kafkaRunner := &invokerkafka.Runner{
		InstanceID:  c.ID,
		KafkaWriter: kafkaWriter,
		InnerRunner: innerRunner,
	}
	httpRunner := &invokerhttp.Runner{
		InstanceID:  c.ID,
		Logger:      logger,
		Decryptor:   decryptor,
		InnerRunner: innerRunner,
	}

	signalCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer cancel()

	// Create an "inner" context that's not directly attached to the signal context. We
	// "manually" propagate that signal below in the shutdown logic.
	ctx, cancelInnerWork := context.WithCancel(context.Background())
	grp, ctx := errgroup.WithContext(ctx)

	// Create a "helper" context, that's not directly attached to the signal context. We
	// cancel this context once all invocations are done to shut down secondary systems.
	helperCtx, cancelHelperWork := context.WithCancel(context.Background())

	// Start Informers first to ensure it's synced and thus we don't miss setting
	// up firewalls for any containers.
	coreInformerFactory.Start(helperCtx.Done())
	coreInformerFactory.WaitForCacheSync(signalCtx.Done())

	// Force a first reconciliation of the firewall.
	if err := firewallReconciler.Reconcile(signalCtx); err != nil {
		logger.Fatal().Err(err).Msg("failed to reconcile firewall")
	}
	go func() {
		// TODO: This is out of an abundance of caution wrt. keeping the same semantics as the
		// firewall pod before. The biggest concern is us needing the "fixups" wrt. the chain
		// ordering. Logs will tell if we even need this.
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-firewallPoke:
				logger.Info().Msg("Reconciling firewall due to state change")
			case <-ticker.C:
				logger.Info().Msg("Reconciling firewall due to timer")
			}
			if err := firewallReconciler.Reconcile(context.Background()); err != nil {
				logger.Error().Err(err).Msg("failed to reconcile firewall")
			}
		}
	}()

	// Worker pool to actually get the activation messages, parse them and proceed with the actual
	// invocation.
	// At this point, we don't know yet how much capacity the message we're reading will require
	// when being executed and thus we start as many workers as we could possibly need if all
	// functions used the minimum amount of memory possible. The ContainerPool will make sure that
	// we never exceed the actual limit.
	// Due to this mismatch, in an overload scenario, we could pull and commit more messages than
	// we can execute at that moment. A catastrophic crash in that moment could cause data-loss.
	workers := int(c.UserMemory / c.MinimumFunctionMemory)
	if len(usableIPs) < workers {
		logger.Fatal().Int("workers", workers).Int("usableIPs", len(usableIPs)).Msg("not enough IPs for the theoretical limit of containers")
	}
	logger.Info().Int("workers", workers).Msg("starting workers")
	metricKafkaDelayInvoker := metricKafkaDelay.With(prometheus.Labels{"topic": topic})
	var lastOffset atomic.Int64
	for i := 0; i < workers; i++ {
		grp.Go(func() error {
			for {
				// ReadMessage commits the message immediately.
				msg, err := kafkaReader.ReadMessage(ctx)
				if err != nil {
					// Check if we're shutting down gracefully.
					if errors.Is(err, context.Canceled) {
						return nil
					}

					// Only log errors but continue looping. If we'd return, the invoker would shut
					// down and restart, which doesn't buy us anything wrt. fixing the underlying
					// problem of this error happening in the first place. Kafka should just be able
					// to eventually reconnect.
					logger.Error().Err(err).Msg("failed to read message from Kafka")
					continue
				}
				kafkaDelay := time.Since(msg.Time).Seconds()
				// This value can be negative due to clock drift between the invoker and the
				// controller or kafka.
				if kafkaDelay < 0 {
					kafkaDelay = 0
				}
				metricKafkaDelayInvoker.Observe(kafkaDelay)

				// Store the highest offset we've seen. Since the workers run in parallel, the
				// messages are not guaranteed to pass in order from this point on.
				offset := msg.Offset + 1
				for {
					current := lastOffset.Load()
					if offset <= current {
						break
					}
					if lastOffset.CompareAndSwap(current, offset) {
						break
					}
				}

				parsed, err := protocol.ParseActivationMessage(msg.Value, decryptor)
				if err != nil {
					logger.Error().Err(err).Msg("failed to parse activation message")
					continue
				}

				// Setup contextual logger. This logger is passed through the entire invocation
				// and all log lines directly related to the invocation will have the respective
				// fields on them.
				loggerCtx := logger.With().
					Str("tid", parsed.TransactionID).
					Str("aid", parsed.ActivationID).
					Str("account", parsed.InvocationIdentity.Subject).
					Str("namespace", parsed.InvocationIdentity.Namespace).
					Str("function", parsed.Function.Name)

				// Run the function. Pass a new context as we don't want to abort inflight invocations.
				kafkaRunner.Invoke(context.Background(), loggerCtx.Logger(), parsed)
			}
		})
	}

	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name:        "gauge_kafka_topic_counter",
		ConstLabels: prometheus.Labels{"topic": topic},
	}, func() float64 {
		lastCommittedOffset := lastOffset.Load()
		if lastCommittedOffset == 0 {
			return 0
		}

		lastOffsetOnTopic, err := invokerkafka.FetchLastOffset(kafkaClient, topic)
		if err != nil {
			logger.Error().Err(err).Msg("failed to read offsets from kafka")
			return 0
		}

		lag := float64(lastOffsetOnTopic - lastCommittedOffset)
		if lag < 0 {
			return 0
		}
		return lag
	})

	// Periodically fetch and update blocked namespaces.
	grp.Go(func() error {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				blockedNamespaces, err := couchDBClient.GetBlockedNamespaces(ctx)
				if err != nil {
					logger.Error().Err(err).Msg("failed to fetch blocked namespaces")
				}

				kafkaRunner.BlockedNamespacesMux.Lock()
				kafkaRunner.BlockedNamespaces = blockedNamespaces
				kafkaRunner.BlockedNamespacesMux.Unlock()

				logger.Info().Int("blockedNamespaces", len(blockedNamespaces)).Msg("updated set of blocked namespaces")
			case <-ctx.Done():
				// We're shutting down.
				return nil
			}
		}
	})

	// HTTP server, serving the /ping endpoint for health...
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	// ... /invoke for invoking Functions...
	mux.Handle("/invoke", httpRunner)
	// ... /metrics for Prometheus metrics...
	mux.Handle("/metrics", promhttp.Handler())
	// ... the /debug/pprof endpoints for live profiling.
	mux.Handle("/debug/pprof/", http.HandlerFunc(pprof.Index))
	mux.Handle("/debug/pprof/cmdline", http.HandlerFunc(pprof.Cmdline))
	mux.Handle("/debug/pprof/profile", http.HandlerFunc(pprof.Profile))
	mux.Handle("/debug/pprof/symbol", http.HandlerFunc(pprof.Symbol))
	mux.Handle("/debug/pprof/trace", http.HandlerFunc(pprof.Trace))
	server := &http.Server{
		Addr:              ":8443",
		ReadHeaderTimeout: 1 * time.Second, // Defense against Slowloris.
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: fmt.Sprintf("invoker0-blue-%d.invoker0-blue.openwhisk.svc.cluster.local", c.ID),
		},
		Handler: mux,
	}
	grp.Go(func() error {
		err := server.ListenAndServeTLS("/invokercerts/tls.crt", "/invokercerts/tls.key")
		// This error happens during "normal execution".
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		// ListenAndServe always returns a non-nil error.
		return fmt.Errorf("failed running HTTP health server: %w", err)
	})

	// Wait for either a signal to arrive or the "inner" context to be done before initiating
	// any shutdown logic.
	// The inner context would be done if any of the goroutines of the group above return an
	// error, which we handle as a catastrophic failure.
	select {
	case <-signalCtx.Done():
	case <-ctx.Done():
	}

	// Wait for all controllers to acknowledge the goodbye.
	sleep := 5 * time.Second
	logger.Info().Msgf("Sleeping for %s to wait for controllers to acknowledge", sleep)
	time.Sleep(sleep)

	// Stop all of the "inner" work.
	cancelInnerWork()
	logger.Err(server.Shutdown(context.Background())).Msg("shutdown server")

	// Wait for all goroutines to have stopped. Once we reach here, we know that no invocations are
	// in-flight anymore. The worker goroutines have shut down by now so it's safe to start the
	// cleanup.
	logger.Err(grp.Wait()).Msg("shutdown all goroutines")

	// Now that all invocations are done, shut down secondary systems.
	cancelHelperWork()

	// Shut down the pool, which deletes all containers.
	logger.Err(containerPool.Shutdown(context.Background())).Msg("shutdown containerpool")

	// Shut down clients.
	logger.Err(kafkaReader.Close()).Msg("shutdown kafka reader")
	logger.Err(kafkaWriter.Close()).Msg("shutdown kafka writer")
	activationWriter.Shutdown()
	logger.Info().Msg("shutdown activation writer")
	coreInformerFactory.Shutdown()
	logger.Info().Msg("shutdown core informers")
}

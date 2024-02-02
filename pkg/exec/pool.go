package exec

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/go-multierror"
	"github.com/rs/zerolog"
	"golang.org/x/sync/semaphore"

	"invoker/pkg/logging"
	"invoker/pkg/protocol"
	"invoker/pkg/units"
)

//go:generate mockgen -package exec -source=pool.go -destination pool_mock.go

const (
	// pauseGrace defines the time we wait before actually pausing a container. If it gets reused
	// before that, the pause operation is skipped.
	pauseGrace = 150 * time.Millisecond
)

// Firewall is an interface to determine if firewalls need to be setup and if so to set them up
// and eventually remove them again.
type Firewall interface {
	NeedsPerContainerFirewallSetup(namespace string) bool
	SetupPerContainerFirewall(ctx context.Context, containerIP string, namespace string) (func(context.Context) error, error)
}

// ContainerPool is a pool of containers, that can be handed out for specific users and functions.
// Its goal is to minimize the amount of cold-starts (freshly created containers) as that's one of
// the main latency drivers.
// The ContainerPool also makes sure that the maximum amount of memory defined for the invoker is
// not exceeded.
type ContainerPool struct {
	logger zerolog.Logger

	factory          ContainerFactory
	runtimeManifests RuntimeManifests
	maxMemory        units.ByteSize

	containersCreated atomic.Uint64

	inFlight       *semaphore.Weighted
	inFlightMemory atomic.Uint64

	idlesMux    sync.Mutex
	idlesMemory units.ByteSize
	idles       map[WarmContainerKey][]*PooledContainer

	prewarmMux  sync.Mutex
	prewarmed   map[PrewarmContainerKey][]*PooledContainer
	prewarmPoke chan struct{}

	grp         sync.WaitGroup
	closeReaper chan struct{}

	containersToDeleteForSpace chan *PooledContainer

	firewall Firewall
}

// WarmContainerKey is the data identifying warm containers suitable to invoke a given function.
type WarmContainerKey struct {
	InvocationNamespace string
	Function            protocol.ActivationMessageFunction
}

// PrewarmContainerKey defines which data points an invocation has to match to be eligible to a given
// prewarm container.
type PrewarmContainerKey struct {
	Kind   string
	Memory units.ByteSize
}

// PooledContainer is a wrapper around a container that has additional fields to facilitate
// management by the pool.
type PooledContainer struct {
	// Container is the underlying container.
	Container

	initializedFor WarmContainerKey
	lastUsed       time.Time
	memory         units.ByteSize

	firewallCleanup func(context.Context) error
}

// ContainerBrokenDuringReuseError marks a failure to reuse a container.
type ContainerBrokenDuringReuseError struct {
	// State is the state that the container is in.
	State ContainerState
	// cause is the underlying error that caused us to think the container is
	// broken in the first place.
	cause error
}

// Error implements error.
func (e ContainerBrokenDuringReuseError) Error() string {
	return fmt.Sprintf("container is broken, likely due to user action, state: %+v, cause: %s", e.State, e.cause.Error())
}

// Unwrap implements error.
func (e ContainerBrokenDuringReuseError) Unwrap() error {
	return e.cause
}

// Remove overrides the inner container's Remove to do our own cleanup first, if necessary.
func (c *PooledContainer) Remove(ctx context.Context) error {
	if c.firewallCleanup == nil {
		return c.Container.Remove(ctx)
	}

	if err := c.firewallCleanup(ctx); err != nil {
		// Actually abort instead of removing the container here. This'll cause the container to
		// "leak" and thus block the respective IP address to avoid the firewall lifecycle breakage
		// to result in another user being able to use the respective per-container-firewall rules.
		return fmt.Errorf("failed to remove firewall: %w. The container is not removed", err)
	}
	if err := c.Container.Remove(ctx); err != nil {
		return fmt.Errorf("failed to remove container: %w", err)
	}
	return nil
}

// NewContainerPool creates a ContainerPool with the given settings.
func NewContainerPool(logger zerolog.Logger, userMemory units.ByteSize, manifests RuntimeManifests, factory ContainerFactory, firewall Firewall, maxContainerIdleTime time.Duration) *ContainerPool {
	pool := &ContainerPool{
		logger: logger,

		factory:          factory,
		runtimeManifests: manifests,
		maxMemory:        userMemory,

		inFlight:  semaphore.NewWeighted(int64(userMemory)),
		idles:     make(map[WarmContainerKey][]*PooledContainer),
		prewarmed: make(map[PrewarmContainerKey][]*PooledContainer),

		// Buffer one request to coalesce reconciles while a reconcile is already in-flight.
		prewarmPoke: make(chan struct{}, 1),
		closeReaper: make(chan struct{}),

		// The buffer here controls the maximum amount of containers waiting to be deleted.
		// We want to bound this to avoid excessive memory pressure on the machine.
		containersToDeleteForSpace: make(chan *PooledContainer, 16),

		firewall: firewall,
	}

	// Start a goroutine that removes containers after their respective idle time.
	if maxContainerIdleTime > 0 {
		pool.grp.Add(1)
		go func() {
			defer pool.grp.Done()

			// We don't expect very granular idle times. Checking once per minute should be good enough.
			reaperTicker := time.NewTicker(1 * time.Minute)
			defer reaperTicker.Stop()

			for {
				select {
				case <-reaperTicker.C:
					if err := pool.reap(context.Background(), time.Now().Add(-maxContainerIdleTime)); err != nil {
						logger.Error().Err(err).Msg("reaper failed to delete containers")
					}
				case <-pool.closeReaper:
					// We're shutting down.
					return
				}
			}
		}()
	}

	// Start a goroutine that handles containers that have to be deleted in order to make space for
	// other containers during normal execution.
	pool.grp.Add(1)
	go func() {
		defer pool.grp.Done()

		for container := range pool.containersToDeleteForSpace {
			if err := container.UnpauseIfNeeded(context.Background()); err != nil {
				if !isBrokenByUser(context.Background(), container) {
					err = CleanupContainer(context.Background(), container, fmt.Errorf("failed to unpause container: %w", err))
					pool.logger.Error().Err(err).Str("cid", container.ID()).Msg("failed to unpause container while making space")
					continue
				}
			}
			if err := container.Remove(context.Background()); err != nil {
				pool.logger.Error().Err(err).Str("cid", container.ID()).Msg("failed to delete container while making space")
			}
		}
	}()

	// Start a goroutine that handles prewarming requests as prewarm containers are used.
	pool.grp.Add(1)
	go func() {
		defer pool.grp.Done()

		for range pool.prewarmPoke {
			logger.Debug().Msg("reconciling prewarming configuration")
			if err := pool.reconcilePrewarm(context.Background()); err != nil {
				logger.Error().Err(err).Msg("failed to reconcile prewarming configuration")
				// Retry reconciliation if there was an error.
				select {
				case pool.prewarmPoke <- struct{}{}:
				default:
					// If the write would block, we can safely move on as there's already a poke in the
					// channel to cause a subsequent reconcile.
				}
			}
		}
	}()
	// Do this on bringup to precreate containers immediately.
	pool.prewarmPoke <- struct{}{}

	return pool
}

// Get obtains a Container for the given identity and function. It's important to call `Put`
// with the handed out Container to return it to the pool. Also returns whether the given container
// has been reused or was a fresh/prewarmed container (true for having been reused). Only containers
// that have been returned once qualify as reused.
func (pool *ContainerPool) Get(ctx context.Context, namespace string, fnMeta protocol.ActivationMessageFunction, kind string, memory units.ByteSize) (*PooledContainer, bool, error) {
	// First acquire a "slot" in the pool. We're enforcing the configured upper bound here.
	// From here on, we can assume that we're not exceeding the execution limits of the invoker.
	if err := pool.inFlight.Acquire(ctx, int64(memory)); err != nil {
		return nil, false, fmt.Errorf("failed to acquire semaphore: %w", err)
	}
	inFlightMemory := units.ByteSize(pool.inFlightMemory.Add(uint64(memory)))

	container, isReused, err := pool.getContainer(ctx, namespace, fnMeta, kind, memory, inFlightMemory)
	if err != nil {
		// Release the semaphore and inflight memory.
		pool.inFlightMemory.Add(-uint64(memory))
		pool.inFlight.Release(int64(memory))
		return nil, false, fmt.Errorf("failed to get container: %w", err)
	}
	return container, isReused, nil
}

func (pool *ContainerPool) getContainer(ctx context.Context, namespace string, fnMeta protocol.ActivationMessageFunction, kind string, memory units.ByteSize, inFlightMemory units.ByteSize) (*PooledContainer, bool, error) {
	contextEvent := logging.ContextEvent(ctx)
	data := WarmContainerKey{
		InvocationNamespace: namespace,
		Function:            fnMeta,
	}

	pool.idlesMux.Lock()
	// If there's an idle container matching the current data, return it immediately.
	// OPTIM: We could potentially optimize container access via finer locking of the individual lists.
	if containers := pool.idles[data]; len(containers) > 0 {
		// Pick the most recently used container (aka the last in the list.)
		container := containers[len(containers)-1]
		pool.idles[data] = containers[:len(containers)-1]

		pool.idlesMemory -= container.memory

		// Unlock the mutex before unpausing and potentially setting up a firewall.
		pool.idlesMux.Unlock()

		contextEvent.
			Str("containerType", "warm").
			Str("cid", container.ID()).
			Str("cip", container.IP())

		// Since its an idle container, a pause was definitely scheduled.
		if unpauseErr := container.UnpauseIfNeeded(ctx); unpauseErr != nil {
			state, stateErr := container.State(ctx)
			if stateErr != nil {
				// If this fails, we want to raise a genuine error.
				return nil, false, fmt.Errorf("failed to unpause idle container: %w. The subsequent state-fetching failed with: %w", unpauseErr, stateErr)
			}

			contextEvent.Interface("cstate", state)

			// If the container is broken, we remove it as it is no longer valid.
			// If the container stopped, the IP might already be reassigned so reusing it at this
			// point is dangerous!
			if removeErr := container.Remove(ctx); removeErr != nil {
				// If this fails, we want to raise a genuine error.
				return nil, false, fmt.Errorf("failed to unpause idle container: %w, state = %+v. The subsequent remove failed with %w", unpauseErr, state, removeErr)
			}

			return nil, false, ContainerBrokenDuringReuseError{State: state, cause: fmt.Errorf("failed to unpause idle container: %w", unpauseErr)}
		}

		needsPerContainerFirewallSetup := pool.firewall.NeedsPerContainerFirewallSetup(namespace)
		if container.firewallCleanup == nil && needsPerContainerFirewallSetup {
			firewallSetupStart := time.Now()
			cleanup, err := pool.firewall.SetupPerContainerFirewall(ctx, container.IP(), namespace)
			if err != nil {
				return nil, false, CleanupContainer(ctx, container, fmt.Errorf("failed to setup firewall for warm container: %w", err))
			}
			container.firewallCleanup = cleanup
			contextEvent.Dur("firewallSetupTime", time.Since(firewallSetupStart))
		} else if container.firewallCleanup != nil && !needsPerContainerFirewallSetup {
			// If there's no per-container-firewall setup for this namespace anymore,
			// we have to delete the firewall setup eagerly to ensure that this
			// container can't reach a proxy that might get the same IP as the
			// proxy this once used.
			firewallCleanupStart := time.Now()
			if err := container.firewallCleanup(ctx); err != nil {
				return nil, false, CleanupContainer(ctx, container, fmt.Errorf("failed to remove firewall for warm container: %w", err))
			}
			container.firewallCleanup = nil
			contextEvent.Dur("firewallCleanupTime", time.Since(firewallCleanupStart))
		}
		contextEvent.Bool("hasPerContainerFirewall", container.firewallCleanup != nil)

		metricContainerObtainedWarm.Inc()
		return container, true, nil
	}

	// Since we can't take a container from the idles pool, we which will extend the idles pool.
	// If that extension exceeds the maximum memory defined, we have to make space for it first.
	var containersToDelete []*PooledContainer
	if pool.idlesMemory+inFlightMemory > pool.maxMemory {
		for {
			var longestUnusedContainer *PooledContainer
			for _, containers := range pool.idles {
				if len(containers) == 0 {
					continue
				}

				// Since the individual container lists are already sorted by lastUsed (beginning
				// with the longest unused), we find the longest unused from all of the functions
				// we're managing and remove that.
				if longestUnusedContainer == nil || containers[0].lastUsed.Before(longestUnusedContainer.lastUsed) {
					longestUnusedContainer = containers[0]
				}
			}

			// We're guaranteed to find enough space in the pool, since we're guarded by a semaphore
			// making sure that there can never be too much in-flight overall. This is just defensive
			// programming.
			if longestUnusedContainer == nil {
				return nil, false, fmt.Errorf("unable to make enough space in container pool")
			}

			containersToDelete = append(containersToDelete, longestUnusedContainer)
			pool.idles[longestUnusedContainer.initializedFor] = pool.idles[longestUnusedContainer.initializedFor][1:]
			pool.idlesMemory -= longestUnusedContainer.memory

			if pool.idlesMemory+inFlightMemory <= pool.maxMemory {
				break
			}
		}
	}
	pool.idlesMux.Unlock()

	if len(containersToDelete) > 0 {
		// The containers are removed by a concurrent process. This **can** backpressure if the
		// channel's buffer is reached. That's by design and the respective time is reported below.
		deleteStart := time.Now()

		for _, container := range containersToDelete {
			pool.containersToDeleteForSpace <- container
		}

		contextEvent.
			Dur("deleteTime", time.Since(deleteStart)).
			Int("deleteNum", len(containersToDelete))
	}

	// See if there's a prewarm container.
	prewarmData := PrewarmContainerKey{
		Kind:   kind,
		Memory: memory,
	}
	pool.prewarmMux.Lock()
	if containers := pool.prewarmed[prewarmData]; len(containers) > 0 {
		container := containers[0]                   // Just take the first container...
		pool.prewarmed[prewarmData] = containers[1:] // ... and keep the rest.

		// Unlock after "taking" the container but before potentially setting up firewalls etc.
		pool.prewarmMux.Unlock()

		// Ensure the prewarm configuration is reconciled.
		select {
		case pool.prewarmPoke <- struct{}{}:
		default:
			// If the write would block, we can safely move on as there's already a poke in the
			// channel to cause a subsequent reconcile.
		}

		if pool.firewall.NeedsPerContainerFirewallSetup(namespace) {
			firewallSetupStart := time.Now()
			cleanup, err := pool.firewall.SetupPerContainerFirewall(ctx, container.IP(), namespace)
			if err != nil {
				return nil, false, CleanupContainer(ctx, container, fmt.Errorf("failed to setup firewall for prewarm container: %w", err))
			}
			container.firewallCleanup = cleanup
			contextEvent.Dur("firewallSetupTime", time.Since(firewallSetupStart))
		}

		contextEvent.
			Str("containerType", "prewarm").
			Bool("hasPerContainerFirewall", container.firewallCleanup != nil).
			Str("cid", container.ID()).
			Str("cip", container.IP())

		container.initializedFor = data // Ensure this is bucketed correctly when returned.
		metricContainerObtainedPrewarm.Inc()
		return container, false, nil
	}
	pool.prewarmMux.Unlock()

	// We'll have to create a new container.
	containerNumber := pool.containersCreated.Add(1)
	containerName := specificContainerName(containerNumber, namespace, fnMeta.Name)
	container, err := pool.factory.CreateContainer(ctx, kind, memory, containerName)
	if err != nil {
		return nil, false, fmt.Errorf("failed to create a container: %w", err)
	}
	pooled := &PooledContainer{
		Container: container,

		initializedFor: data,
		memory:         memory,
	}

	if pool.firewall.NeedsPerContainerFirewallSetup(namespace) {
		firewallSetupStart := time.Now()
		cleanup, err := pool.firewall.SetupPerContainerFirewall(ctx, container.IP(), namespace)
		if err != nil {
			return nil, false, CleanupContainer(ctx, container, fmt.Errorf("failed to setup firewall for cold container: %w", err))
		}
		pooled.firewallCleanup = cleanup
		contextEvent.Dur("firewallSetupTime", time.Since(firewallSetupStart))
	}

	contextEvent.
		Str("containerType", "cold").
		Bool("hasPerContainerFirewall", pooled.firewallCleanup != nil).
		Str("cid", container.ID()).
		Str("cip", container.IP())

	metricContainerObtainedCold.Inc()
	return pooled, false, nil
}

// Put returns the given container to the pool. If it is marked for deletion, it will be deleted
// instead and not be returned to the pool.
func (pool *ContainerPool) Put(_ context.Context, container *PooledContainer) {
	// Release the capacity from the pool last (defer are executed in reverse order)
	defer pool.inFlight.Release(int64(container.memory))
	defer pool.inFlightMemory.Add(-uint64(container.memory))

	// We're still the sole owners of the reference, so it's safe to do these outside of the lock.
	container.lastUsed = time.Now()
	container.PauseEventually(pauseGrace)

	pool.idlesMux.Lock()
	defer pool.idlesMux.Unlock()
	pool.idlesMemory += container.memory
	pool.idles[container.initializedFor] = append(pool.idles[container.initializedFor], container)
}

// PutToDestroy returns the given container to the pool to release the attached resources and then
// destroys the container.
func (pool *ContainerPool) PutToDestroy(ctx context.Context, container *PooledContainer) error {
	// Release the capacity from the pool last (defer are executed in reverse order)
	defer pool.inFlight.Release(int64(container.memory))
	defer pool.inFlightMemory.Add(-uint64(container.memory))

	// No need to try to unpause the container here. We know it's not paused as it'd only be
	// paused below.
	if err := container.Remove(ctx); err != nil {
		return fmt.Errorf("failed to delete container when returning it: %w", err)
	}
	return nil
}

// reap removes containers older than the given time from the idle pool.
func (pool *ContainerPool) reap(ctx context.Context, olderThan time.Time) error {
	// First, only collect the containers we want to reap. We're going to make the deletion calls
	// outside of the lock.
	var toDelete []*PooledContainer
	pool.idlesMux.Lock()
	for data, containers := range pool.idles {
		// The container list is ordered by lastUsed (the last container in the list being the
		// most recently used). Thus, if there's containers to remove, we can always take a
		// subslice to the end of the list.
		deleteUntil := 0
		for ; deleteUntil < len(containers); deleteUntil++ {
			container := containers[deleteUntil]
			if container.lastUsed.After(olderThan) {
				// The first container that's young enough marks the cutoff point.
				break
			}
			// Update the idlesMemory bookkeeping as this container will be deleted.
			pool.idlesMemory -= container.memory
		}
		// Add the prefix to the deletion list...
		toDelete = append(toDelete, containers[:deleteUntil]...)
		toKeep := containers[deleteUntil:]
		if len(toKeep) > 0 {
			// ... and keep the rest, if there's anything left.
			pool.idles[data] = toKeep
		} else {
			// Clear the map otherwise, to avoid memory leaks.
			delete(pool.idles, data)
		}
	}
	pool.idlesMux.Unlock()

	var errs *multierror.Error
	for _, container := range toDelete {
		pool.logger.Info().Str("cid", container.ID()).Msg("deleting container due to it being too old")
		if err := container.UnpauseIfNeeded(ctx); err != nil {
			if !isBrokenByUser(ctx, container) {
				errs = multierror.Append(errs, CleanupContainer(ctx, container, fmt.Errorf("failed to unpause container during reaping: %w", err)))
				continue
			}
		}
		if err := container.Remove(ctx); err != nil {
			errs = multierror.Append(errs, fmt.Errorf("failed to delete container during reaping: %w", err))
		}
	}
	return errs.ErrorOrNil()
}

// reconcilePrewarm makes sure that we have prewarm containers according to our config. As the name
// suggests, its written in a reconciler-fashion meaning that it doesn't need any "input state" and
// instead always operates on the current state of the pool. In essence, that means we don't need to
// catch every instance of using a container and can instead coalesce multiple events into a single
// reconciliation.
func (pool *ContainerPool) reconcilePrewarm(ctx context.Context) error {
	// Find out which containers we'd have to create now, under the lock.
	necessaryCreations := make(map[PrewarmContainerKey]int)
	pool.prewarmMux.Lock()
	for family, runtimeManifest := range pool.runtimeManifests {
		// Skip :default runtimes in favor of their explicit counterparts.
		if strings.HasSuffix(family, ":default") {
			continue
		}
		for _, stemcellConfig := range runtimeManifest.Stemcells {
			prewarmData := PrewarmContainerKey{
				Kind:   runtimeManifest.Kind,
				Memory: stemcellConfig.Memory,
			}
			necessaryCreations[prewarmData] = stemcellConfig.Count - len(pool.prewarmed[prewarmData])
		}
	}
	pool.prewarmMux.Unlock()

	// Create all necessary containers outside of the lock to allow parallel invocations to still
	// consume prewarm containers.
	// Inherently, this can race with the consumption of further prewarm containers. In such a case,
	// we'd be having not enough prewarm containers after the reconciliation, but another
	// reconciliation will be triggered right away by the consumption code, which will then
	// eventually make the amount of prewarm containers consistent again.
	for prewarmData, toCreate := range necessaryCreations {
		for i := 0; i < toCreate; i++ {
			pool.logger.Debug().Msgf("creating new prewarm container for %s with %s", prewarmData.Kind, prewarmData.Memory)
			containerNumber := pool.containersCreated.Add(1)
			containerName := prewarmContainerName(containerNumber, prewarmData.Kind)
			container, err := pool.factory.CreateContainer(ctx, prewarmData.Kind, prewarmData.Memory, containerName)
			if err != nil {
				return fmt.Errorf("failed to create prewarm container: %w", err)
			}
			pooledContainer := &PooledContainer{
				Container: container,

				memory: prewarmData.Memory,
			}

			// Add prewarm containers to the respective list directly after creation to allow them
			// to be used right away.
			pool.prewarmMux.Lock()
			pool.prewarmed[prewarmData] = append(pool.prewarmed[prewarmData], pooledContainer)
			pool.prewarmMux.Unlock()
		}
	}
	return nil
}

// Shutdown shuts the pool down and removes all existing containers.
func (pool *ContainerPool) Shutdown(ctx context.Context) error {
	// Close the background jobs first to avoid races.
	close(pool.prewarmPoke)
	close(pool.closeReaper)
	close(pool.containersToDeleteForSpace)
	pool.grp.Wait()

	pool.idlesMux.Lock()
	defer pool.idlesMux.Unlock()

	var errs *multierror.Error
	for _, containers := range pool.idles {
		for _, container := range containers {
			pool.logger.Info().Str("cid", container.ID()).Msg("deleting idle container due to shutdown")
			if err := container.UnpauseIfNeeded(ctx); err != nil {
				if !isBrokenByUser(ctx, container) {
					errs = multierror.Append(errs, CleanupContainer(ctx, container, fmt.Errorf("failed to unpause idle container: %w", err)))
					continue
				}
			}
			if err := container.Remove(ctx); err != nil {
				errs = multierror.Append(errs, fmt.Errorf("failed to delete idle container: %w", err))
			}
		}
	}

	pool.prewarmMux.Lock()
	defer pool.prewarmMux.Unlock()

	for _, containers := range pool.prewarmed {
		for _, container := range containers {
			pool.logger.Info().Str("cid", container.ID()).Msg("deleting prewarm container due to shutdown")
			// Prewarm containers are never paused, thus we don't have to try to unpause.
			if err := container.Remove(ctx); err != nil {
				errs = multierror.Append(errs, fmt.Errorf("failed to delete prewarm container: %w", err))
			}
		}
	}

	return errs.ErrorOrNil()
}

// specificContainerName generates a name for a container specific to a namespace and action.
func specificContainerName(i uint64, namespace string, action string) string {
	return fmt.Sprintf("wsk_%d_%s_%s", i, sanitizeForContainerName(namespace), sanitizeForContainerName(action))
}

// prewarmContainerName generates a name for a prewarmed container.
func prewarmContainerName(i uint64, kind string) string {
	return fmt.Sprintf("wsk_%d_prewarm_%s", i, sanitizeForContainerName(kind))
}

var (
	// Inverted version of https://github.com/moby/moby/blob/6eab4f55faf77bb0f253553d7d6fadb2ef6f4669/daemon/names/names.go#L6.
	validContainerName = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)
)

// sanitizeForContainerName removes all disallowed characters from the given string.
func sanitizeForContainerName(part string) string {
	return validContainerName.ReplaceAllLiteralString(part, "")
}

// isBrokenByUser returns true if the container was put in a state that would've been caused by the
// user, like running out of memory or exiting.
func isBrokenByUser(ctx context.Context, container Container) bool {
	state, err := container.State(ctx)
	if err != nil {
		// Assume it's not caused by the user if we fail to fetch the container's state for whatever
		// reason.
		return false
	}
	return state.OOMKilled || state.Status == ContainerStateStatusExited
}

package exec

import (
	"context"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/hashicorp/go-multierror"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/semaphore"

	"invoker/pkg/protocol"
	"invoker/pkg/units"
)

var (
	testKind     = "nodejs:foo"
	testManifest = RuntimeManifests{
		testKind: RuntimeManifest{
			Image: RuntimeImage("a.test.registry/nodejs:foo"),
		},
	}
)

func TestContainerPoolReusesContainers(t *testing.T) {
	t.Parallel()

	fn1Meta := protocol.ActivationMessageFunction{
		Namespace: "test1",
		Name:      "func",
		Revision:  "abc",
	}
	fn2Meta := protocol.ActivationMessageFunction{
		Namespace: "test2",
		Name:      "func",
		Revision:  "cbd",
	}

	ctrl := gomock.NewController(t)
	factory := NewMockContainerFactory(ctrl)
	pool := NewContainerPool(zerolog.Nop(), 4*units.Gigabyte, testManifest, factory, &noopFirewall{}, 0)

	// Invoking the first function will create a container.
	ctr1 := newMockContainer(ctrl)
	factory.EXPECT().CreateContainer(gomock.Any(), testKind, 128*units.Megabyte, "wsk_1_invocationNamespace_func").Return(ctr1, nil)
	fn1Container, isReused, err := pool.Get(context.Background(), "invocationNamespace", fn1Meta, "nodejs:foo", 128*units.Megabyte)
	require.NoError(t, err)
	require.Equal(t, ctr1, fn1Container.Container)
	require.False(t, isReused)

	// Returning it will add it to the pool.
	ctr1.EXPECT().PauseEventually(pauseGrace)
	pool.Put(context.Background(), fn1Container)

	// Invoking it again will return the same container.
	ctr1.EXPECT().UnpauseIfNeeded(gomock.Any())
	fn1ContainerSame, isReused, err := pool.Get(context.Background(), "invocationNamespace", fn1Meta, "nodejs:foo", 128*units.Megabyte)
	require.NoError(t, err)
	require.Equal(t, fn1Container, fn1ContainerSame)
	require.True(t, isReused)

	// Invoking the same function again will cause a new container to be created (because the other
	// wasn't returned yet).
	ctr2 := newMockContainer(ctrl)
	factory.EXPECT().CreateContainer(gomock.Any(), testKind, 128*units.Megabyte, "wsk_2_invocationNamespace_func").Return(ctr2, nil)
	fn1Container2, isReused, err := pool.Get(context.Background(), "invocationNamespace", fn1Meta, "nodejs:foo", 128*units.Megabyte)
	require.NoError(t, err)
	require.Equal(t, ctr2, fn1Container2.Container)
	require.False(t, isReused)

	// Now return both containers again.
	ctr1.EXPECT().PauseEventually(pauseGrace)
	pool.Put(context.Background(), fn1ContainerSame)
	ctr2.EXPECT().PauseEventually(pauseGrace)
	pool.Put(context.Background(), fn1Container2)

	// Invoking the same function from another namespace creates a new container.
	ctr3 := newMockContainer(ctrl)
	factory.EXPECT().CreateContainer(gomock.Any(), testKind, 128*units.Megabyte, "wsk_3_differentNamespace_func").Return(ctr3, nil)
	fn1DifferentNamespace, isReused, err := pool.Get(context.Background(), "differentNamespace", fn1Meta, "nodejs:foo", 128*units.Megabyte)
	require.NoError(t, err)
	require.Equal(t, ctr3, fn1DifferentNamespace.Container)
	require.False(t, isReused)

	ctr3.EXPECT().PauseEventually(pauseGrace)
	pool.Put(context.Background(), fn1DifferentNamespace)

	// Invoking another function in the same namespace also creates a new container.
	ctr4 := newMockContainer(ctrl)
	factory.EXPECT().CreateContainer(gomock.Any(), testKind, 128*units.Megabyte, "wsk_4_invocationNamespace_func").Return(ctr4, nil)
	fn2Container, isReused, err := pool.Get(context.Background(), "invocationNamespace", fn2Meta, "nodejs:foo", 128*units.Megabyte)
	require.NoError(t, err)
	require.Equal(t, ctr4, fn2Container.Container)
	require.False(t, isReused)

	ctr4.EXPECT().PauseEventually(pauseGrace)
	pool.Put(context.Background(), fn2Container)
}

func TestContainerPoolFreesCapacity(t *testing.T) {
	t.Parallel()

	fnMeta := protocol.ActivationMessageFunction{
		Namespace: "test1",
		Name:      "func",
		Revision:  "abc",
	}
	ctrl := gomock.NewController(t)
	factory := NewMockContainerFactory(ctrl)
	pool := NewContainerPool(zerolog.Nop(), 3*units.Megabyte, testManifest, factory, &noopFirewall{}, 0)

	// Create containers to be at capacity.
	ctr1 := newMockContainer(ctrl)
	factory.EXPECT().CreateContainer(gomock.Any(), testKind, 1*units.Megabyte, "wsk_1_invocationNamespace_func").Return(ctr1, nil)
	container, _, err := pool.Get(context.Background(), "invocationNamespace", fnMeta, "nodejs:foo", 1*units.Megabyte)
	require.NoError(t, err)
	ctr2 := newMockContainer(ctrl)
	factory.EXPECT().CreateContainer(gomock.Any(), testKind, 1*units.Megabyte, "wsk_2_invocationNamespace_func").Return(ctr2, nil)
	container2, _, err := pool.Get(context.Background(), "invocationNamespace", fnMeta, "nodejs:foo", 1*units.Megabyte)
	require.NoError(t, err)
	ctr3 := newMockContainer(ctrl)
	factory.EXPECT().CreateContainer(gomock.Any(), testKind, 1*units.Megabyte, "wsk_3_invocationNamespace_func").Return(ctr3, nil)
	container3, _, err := pool.Get(context.Background(), "invocationNamespace", fnMeta, "nodejs:foo", 1*units.Megabyte)
	require.NoError(t, err)

	// Put them all back into the pool to make it be at capacity.
	ctr1.EXPECT().PauseEventually(pauseGrace)
	pool.Put(context.Background(), container)
	ctr2.EXPECT().PauseEventually(pauseGrace)
	pool.Put(context.Background(), container2)
	ctr3.EXPECT().PauseEventually(pauseGrace)
	pool.Put(context.Background(), container3)

	// Invoking the function in another namespace will require the first 2 to be deleted to create capacity.
	ctr1.EXPECT().UnpauseIfNeeded(gomock.Any())
	ctr1.EXPECT().Remove(gomock.Any())
	ctr2.EXPECT().UnpauseIfNeeded(gomock.Any())
	ctr2.EXPECT().Remove(gomock.Any())

	ctr4 := newMockContainer(ctrl)
	factory.EXPECT().CreateContainer(gomock.Any(), testKind, 2*units.Megabyte, "wsk_4_differentNamespace_func").Return(ctr4, nil)
	_, isReused, err := pool.Get(context.Background(), "differentNamespace", fnMeta, "nodejs:foo", 2*units.Megabyte)
	require.NoError(t, err)
	require.False(t, isReused)

	// Wait for the containers to actually be removed.
	require.Eventually(t, func() bool { return len(pool.containersToDeleteForSpace) == 0 }, 1*time.Second, 10*time.Millisecond)
}

func TestContainerPoolRemovesOnReturn(t *testing.T) {
	t.Parallel()

	fnMeta := protocol.ActivationMessageFunction{
		Namespace: "test1",
		Name:      "func",
		Revision:  "abc",
	}
	ctrl := gomock.NewController(t)
	factory := NewMockContainerFactory(ctrl)
	pool := NewContainerPool(zerolog.Nop(), 3*units.Megabyte, testManifest, factory, &noopFirewall{}, 0)

	// Create a container.
	ctr := newMockContainer(ctrl)
	factory.EXPECT().CreateContainer(gomock.Any(), testKind, 1*units.Megabyte, "wsk_1_invocationNamespace_func").Return(ctr, nil)
	container, _, err := pool.Get(context.Background(), "invocationNamespace", fnMeta, "nodejs:foo", 1*units.Megabyte)
	require.NoError(t, err)

	// Mark it for deletion and return it. Expect it to be deleted.
	ctr.EXPECT().Remove(gomock.Any())
	require.NoError(t, pool.PutToDestroy(context.Background(), container))
}

func TestContainerPoolRemovesOnFailingUnpause(t *testing.T) {
	t.Parallel()

	fnMeta := protocol.ActivationMessageFunction{
		Namespace: "test1",
		Name:      "func",
		Revision:  "abc",
	}

	ctrl := gomock.NewController(t)
	factory := NewMockContainerFactory(ctrl)
	pool := NewContainerPool(zerolog.Nop(), 4*units.Gigabyte, testManifest, factory, &noopFirewall{}, 0)

	// Invoking the first function will create a container.
	ctr := newMockContainer(ctrl)
	factory.EXPECT().CreateContainer(gomock.Any(), testKind, 128*units.Megabyte, "wsk_1_invocationNamespace_func").Return(ctr, nil)
	container, isReused, err := pool.Get(context.Background(), "invocationNamespace", fnMeta, "nodejs:foo", 128*units.Megabyte)
	require.NoError(t, err)
	require.Equal(t, ctr, container.Container)
	require.False(t, isReused)

	// Returning it will add it to the pool.
	ctr.EXPECT().PauseEventually(pauseGrace)
	pool.Put(context.Background(), container)

	// Invoking it again will try to return the same container. It'll remove it and return an
	// error describing the issue as user-caused.
	ctr.EXPECT().UnpauseIfNeeded(gomock.Any()).Return(assert.AnError)
	ctr.EXPECT().State(gomock.Any()).Return(ContainerState{Status: ContainerStateStatusExited}, nil)
	ctr.EXPECT().Remove(gomock.Any())

	_, _, err = pool.Get(context.Background(), "invocationNamespace", fnMeta, "nodejs:foo", 128*units.Megabyte)
	var errUserCaused ContainerBrokenDuringReuseError
	require.ErrorIs(t, err, assert.AnError)
	require.ErrorAs(t, err, &errUserCaused)
	require.Equal(t, errUserCaused.State, ContainerState{Status: ContainerStateStatusExited})
}

func TestContainerPoolShutdownDeletesEverything(t *testing.T) {
	t.Parallel()

	fnMeta := protocol.ActivationMessageFunction{
		Namespace: "test1",
		Name:      "func",
		Revision:  "abc",
	}
	ctrl := gomock.NewController(t)
	factory := NewMockContainerFactory(ctrl)
	pool := NewContainerPool(zerolog.Nop(), 5*units.Megabyte, testManifest, factory, &noopFirewall{}, 0)

	// Create 3 containers.
	ctr1 := newMockContainer(ctrl)
	factory.EXPECT().CreateContainer(gomock.Any(), testKind, 1*units.Megabyte, "wsk_1_invocationNamespace_func").Return(ctr1, nil)
	container, _, err := pool.Get(context.Background(), "invocationNamespace", fnMeta, "nodejs:foo", 1*units.Megabyte)
	require.NoError(t, err)
	ctr2 := newMockContainer(ctrl)
	factory.EXPECT().CreateContainer(gomock.Any(), testKind, 1*units.Megabyte, "wsk_2_invocationNamespace_func").Return(ctr2, nil)
	container2, _, err := pool.Get(context.Background(), "invocationNamespace", fnMeta, "nodejs:foo", 1*units.Megabyte)
	require.NoError(t, err)
	ctr3 := newMockContainer(ctrl)
	factory.EXPECT().CreateContainer(gomock.Any(), testKind, 1*units.Megabyte, "wsk_3_invocationNamespace_func").Return(ctr3, nil)
	container3, _, err := pool.Get(context.Background(), "invocationNamespace", fnMeta, "nodejs:foo", 1*units.Megabyte)
	require.NoError(t, err)

	// Put them all back into the pool to make them known.
	ctr1.EXPECT().PauseEventually(pauseGrace)
	pool.Put(context.Background(), container)
	ctr2.EXPECT().PauseEventually(pauseGrace)
	pool.Put(context.Background(), container2)
	ctr3.EXPECT().PauseEventually(pauseGrace)
	pool.Put(context.Background(), container3)

	// Expect all containers to be removed on shutdown, even if some fail on certain operations.
	ctr1.EXPECT().UnpauseIfNeeded(gomock.Any()).Return(assert.AnError)
	ctr1.EXPECT().State(gomock.Any()).AnyTimes().Return(ContainerState{OOMKilled: true}, nil) // User-caused, so error is not surfaced.
	ctr1.EXPECT().Remove(gomock.Any())                                                        // Gets removed even though the unpause failed.
	ctr2.EXPECT().UnpauseIfNeeded(gomock.Any())
	ctr2.EXPECT().Remove(gomock.Any()).Return(assert.AnError)
	ctr3.EXPECT().UnpauseIfNeeded(gomock.Any()).Return(assert.AnError)
	// State is called twice because the cleanup logic fetches it again.
	ctr3.EXPECT().State(gomock.Any()).Times(2).Return(ContainerState{OOMKilled: false}, nil) // Not user-caused, error is surfaced.
	ctr3.EXPECT().Remove(gomock.Any())

	// Only 2 errors are surfaced. The user-caused unpause error is swallowed.
	var errs *multierror.Error
	require.ErrorAs(t, pool.Shutdown(context.Background()), &errs)
	require.Len(t, errs.Errors, 2)
}

func TestContainerPoolReapsOldContainers(t *testing.T) {
	t.Parallel()

	fnMeta := protocol.ActivationMessageFunction{
		Namespace: "test1",
		Name:      "func",
		Revision:  "abc",
	}
	ctrl := gomock.NewController(t)
	factory := NewMockContainerFactory(ctrl)
	pool := NewContainerPool(zerolog.Nop(), 5*units.Megabyte, testManifest, factory, &noopFirewall{}, 0)

	// Create 3 containers.
	ctr1 := newMockContainer(ctrl)
	factory.EXPECT().CreateContainer(gomock.Any(), testKind, 1*units.Megabyte, "wsk_1_invocationNamespace_func").Return(ctr1, nil)
	container, _, err := pool.Get(context.Background(), "invocationNamespace", fnMeta, "nodejs:foo", 1*units.Megabyte)
	require.NoError(t, err)
	ctr2 := newMockContainer(ctrl)
	factory.EXPECT().CreateContainer(gomock.Any(), testKind, 1*units.Megabyte, "wsk_2_invocationNamespace_func").Return(ctr2, nil)
	container2, _, err := pool.Get(context.Background(), "invocationNamespace", fnMeta, "nodejs:foo", 1*units.Megabyte)
	require.NoError(t, err)
	ctr3 := newMockContainer(ctrl)
	factory.EXPECT().CreateContainer(gomock.Any(), testKind, 2*units.Megabyte, "wsk_3_invocationNamespace_func").Return(ctr3, nil)
	container3, _, err := pool.Get(context.Background(), "invocationNamespace", fnMeta, "nodejs:foo", 2*units.Megabyte)
	require.NoError(t, err)

	// Put them both back into the pool to make them known.
	ctr1.EXPECT().PauseEventually(pauseGrace)
	pool.Put(context.Background(), container)
	ctr2.EXPECT().PauseEventually(pauseGrace)
	pool.Put(context.Background(), container2)
	reapTime := time.Now() // Taking the timestamp now to keep the third container.
	ctr3.EXPECT().PauseEventually(pauseGrace)
	pool.Put(context.Background(), container3)

	// Reap until the reapTime should remove the first 2 containers, even though the first has issues removing.
	ctr1.EXPECT().UnpauseIfNeeded(gomock.Any())
	ctr1.EXPECT().Remove(gomock.Any()) // Gets removed even though the unpause failed.
	ctr2.EXPECT().UnpauseIfNeeded(gomock.Any())
	ctr2.EXPECT().Remove(gomock.Any()).Return(assert.AnError)
	require.ErrorIs(t, pool.reap(context.Background(), reapTime), assert.AnError)

	// The internal memory counter has been updated accordingly.
	require.Equal(t, 2*units.Megabyte, pool.idlesMemory)

	// Reap again until now should remove the last container.
	ctr3.EXPECT().UnpauseIfNeeded(gomock.Any())
	ctr3.EXPECT().Remove(gomock.Any())
	require.NoError(t, pool.reap(context.Background(), time.Now()))

	// The map key has gotten deleted to avoid memory leaks.
	require.Empty(t, pool.idles)
	// The internal memory counter has been updated accordingly.
	require.Equal(t, 0*units.Byte, pool.idlesMemory)
}

func TestContainerPoolPrewarms(t *testing.T) {
	t.Parallel()

	fnMeta := protocol.ActivationMessageFunction{
		Namespace: "test1",
		Name:      "func",
		Revision:  "abc",
	}

	key := PrewarmContainerKey{
		Kind:   "nodejs:foo",
		Memory: 2 * units.Megabyte,
	}
	runtimeManifest := RuntimeManifest{
		Kind:  key.Kind,
		Image: RuntimeImage("a.test.registry/nodejs:foo"),
		Stemcells: []RuntimeStemcellConfig{{
			Count:  2,
			Memory: key.Memory,
		}},
	}
	prewarmingManifest := RuntimeManifests{
		key.Kind:         runtimeManifest,
		"nodejs:default": runtimeManifest, // This'll be skipped.
	}

	// Manually constructing the pool here to avoid launching the goroutines so we can control
	// the prewarming synchronously.
	ctrl := gomock.NewController(t)
	factory := NewMockContainerFactory(ctrl)

	pool := &ContainerPool{
		logger: zerolog.Nop(),

		factory:          factory,
		runtimeManifests: prewarmingManifest,
		maxMemory:        5 * units.Megabyte,

		inFlight:  semaphore.NewWeighted(int64(5 * units.Megabyte)),
		idles:     make(map[WarmContainerKey][]*PooledContainer),
		prewarmed: make(map[PrewarmContainerKey][]*PooledContainer),

		// Buffer one request to coalesce reconciles while a reconcile is already in-flight.
		prewarmPoke: make(chan struct{}, 1),
		closeReaper: make(chan struct{}),

		// The buffer here controls the maximum amount of containers waiting to be deleted.
		// We want to bound this to avoid excessive memory pressure on the machine.
		containersToDeleteForSpace: make(chan *PooledContainer, 16),

		firewall: &noopFirewall{},
	}

	// Initially, we don't have any prewarmed containers.
	require.Empty(t, pool.prewarmed)

	// Trigger a prewarming reconciliation to create 2 as per the manifest.
	ctr1 := newMockContainer(ctrl)
	factory.EXPECT().CreateContainer(gomock.Any(), testKind, key.Memory, "wsk_1_prewarm_nodejsfoo").Return(ctr1, nil)
	ctr2 := newMockContainer(ctrl)
	factory.EXPECT().CreateContainer(gomock.Any(), testKind, key.Memory, "wsk_2_prewarm_nodejsfoo").Return(ctr2, nil)

	require.NoError(t, pool.reconcilePrewarm(context.Background()))
	require.Len(t, pool.prewarmed[key], 2)

	// Take one.
	container, isReused, err := pool.Get(context.Background(), "invocationNamespace", fnMeta, key.Kind, key.Memory)
	require.NoError(t, err)
	require.Equal(t, ctr1, container.Container)
	require.Len(t, pool.prewarmed[key], 1)
	require.False(t, isReused) // A prewarm container isn't reused just yet.

	// There should've been a prewarming reconciliation request because we've taken a prewarm container.
	<-pool.prewarmPoke

	// Trigger reconciliation again to bring back state as per the manifest.
	ctr3 := newMockContainer(ctrl)
	factory.EXPECT().CreateContainer(gomock.Any(), testKind, key.Memory, "wsk_3_prewarm_nodejsfoo").Return(ctr3, nil)

	require.NoError(t, pool.reconcilePrewarm(context.Background()))
	require.Len(t, pool.prewarmed[key], 2)

	// Shutting down the pool deletes all of the prewarmed containers too.
	ctr2.EXPECT().Remove(gomock.Any()).Return(assert.AnError)
	ctr3.EXPECT().Remove(gomock.Any())
	require.ErrorIs(t, pool.Shutdown(context.Background()), assert.AnError)
}

func TestContainerPoolSetsUpFirewallOnColdStart(t *testing.T) {
	t.Parallel()

	fn1Meta := protocol.ActivationMessageFunction{
		Namespace: "test1",
		Name:      "func",
		Revision:  "abc",
	}

	ctrl := gomock.NewController(t)
	factory := NewMockContainerFactory(ctrl)
	firewall := NewMockFirewall(ctrl)
	pool := NewContainerPool(zerolog.Nop(), 4*units.Gigabyte, testManifest, factory, firewall, 0)

	// Invoking the first function will create a container and setup the firewall for it.
	ctr1 := newMockContainer(ctrl)
	factory.EXPECT().CreateContainer(gomock.Any(), testKind, 128*units.Megabyte, "wsk_1_invocationNamespace_func").Return(ctr1, nil)
	firewall.EXPECT().NeedsPerContainerFirewallSetup("invocationNamespace").Return(true)
	var removeCalled bool
	firewall.EXPECT().SetupPerContainerFirewall(gomock.Any(), gomock.Any(), "invocationNamespace").Return(func(context.Context) error {
		removeCalled = true
		return nil
	}, nil)

	fn1Container, isReused, err := pool.Get(context.Background(), "invocationNamespace", fn1Meta, "nodejs:foo", 128*units.Megabyte)
	require.NoError(t, err)
	require.False(t, isReused)

	// Returning it will add it to the pool.
	ctr1.EXPECT().PauseEventually(pauseGrace)
	pool.Put(context.Background(), fn1Container)

	// Invoking it again will return the same container.
	ctr1.EXPECT().UnpauseIfNeeded(gomock.Any())
	firewall.EXPECT().NeedsPerContainerFirewallSetup("invocationNamespace").Return(true)
	fn1ContainerSame, isReused, err := pool.Get(context.Background(), "invocationNamespace", fn1Meta, "nodejs:foo", 128*units.Megabyte)
	require.NoError(t, err)
	require.Equal(t, fn1Container, fn1ContainerSame)
	require.True(t, isReused)

	// Returning it for destruction should remove the firewall.
	ctr1.EXPECT().Remove(gomock.Any())
	err = pool.PutToDestroy(context.Background(), fn1ContainerSame)
	require.NoError(t, err)
	require.True(t, removeCalled)

	// Invoking it again will try to yield a new container but surface an error if the firewall setup fails.
	ctr2 := newMockContainer(ctrl)
	factory.EXPECT().CreateContainer(gomock.Any(), testKind, 128*units.Megabyte, "wsk_2_invocationNamespace_func").Return(ctr2, nil)
	firewall.EXPECT().NeedsPerContainerFirewallSetup("invocationNamespace").Return(true)
	firewall.EXPECT().SetupPerContainerFirewall(gomock.Any(), gomock.Any(), "invocationNamespace").Return(nil, assert.AnError)
	ctr2.EXPECT().State(gomock.Any())
	ctr2.EXPECT().Remove(gomock.Any())

	_, _, err = pool.Get(context.Background(), "invocationNamespace", fn1Meta, "nodejs:foo", 128*units.Megabyte)
	require.ErrorIs(t, err, assert.AnError)
}

func TestContainerPoolSetsUpFirewallForPrewarm(t *testing.T) {
	t.Parallel()

	fnMeta := protocol.ActivationMessageFunction{
		Namespace: "test1",
		Name:      "func",
		Revision:  "abc",
	}

	key := PrewarmContainerKey{
		Kind:   "nodejs:foo",
		Memory: 2 * units.Megabyte,
	}
	runtimeManifest := RuntimeManifest{
		Kind:  key.Kind,
		Image: RuntimeImage("a.test.registry/nodejs:foo"),
		Stemcells: []RuntimeStemcellConfig{{
			Count:  1,
			Memory: key.Memory,
		}},
	}
	prewarmingManifest := RuntimeManifests{
		key.Kind: runtimeManifest,
	}

	ctrl := gomock.NewController(t)
	factory := NewMockContainerFactory(ctrl)
	firewall := NewMockFirewall(ctrl)

	// Manually constructing the pool here to avoid launching the goroutines so we can control
	// the prewarming synchronously.
	pool := &ContainerPool{
		logger: zerolog.Nop(),

		factory:          factory,
		runtimeManifests: prewarmingManifest,
		maxMemory:        5 * units.Megabyte,

		inFlight:  semaphore.NewWeighted(int64(5 * units.Megabyte)),
		idles:     make(map[WarmContainerKey][]*PooledContainer),
		prewarmed: make(map[PrewarmContainerKey][]*PooledContainer),

		// Buffer one request to coalesce reconciles while a reconcile is already in-flight.
		prewarmPoke: make(chan struct{}, 1),
		closeReaper: make(chan struct{}),

		firewall: firewall,
	}

	// Trigger a prewarming reconciliation to create 1 as per the manifest.
	ctr1 := newMockContainer(ctrl)
	factory.EXPECT().CreateContainer(gomock.Any(), testKind, key.Memory, "wsk_1_prewarm_nodejsfoo").Return(ctr1, nil)

	require.NoError(t, pool.reconcilePrewarm(context.Background()))
	require.Len(t, pool.prewarmed[key], 1)

	// Taking the container from the pool causes a firewall to be setup.
	firewall.EXPECT().NeedsPerContainerFirewallSetup("invocationNamespace").Return(true)
	var removeCalled bool
	firewall.EXPECT().SetupPerContainerFirewall(gomock.Any(), gomock.Any(), "invocationNamespace").Return(func(context.Context) error {
		removeCalled = true
		return nil
	}, nil)

	container, isReused, err := pool.Get(context.Background(), "invocationNamespace", fnMeta, key.Kind, key.Memory)
	require.NoError(t, err)
	require.False(t, isReused)

	// Destroying the container destroys the firewall.
	ctr1.EXPECT().Remove(gomock.Any())
	err = pool.PutToDestroy(context.Background(), container)
	require.NoError(t, err)
	require.True(t, removeCalled)

	// Triggering a prewarming reconciliation will create a new prewarm container.
	ctr2 := newMockContainer(ctrl)
	factory.EXPECT().CreateContainer(gomock.Any(), testKind, key.Memory, "wsk_2_prewarm_nodejsfoo").Return(ctr2, nil)

	require.NoError(t, pool.reconcilePrewarm(context.Background()))

	// Invoking it again will try to yield a new container but surface an error if the firewall setup fails.
	firewall.EXPECT().NeedsPerContainerFirewallSetup("invocationNamespace").Return(true)
	firewall.EXPECT().SetupPerContainerFirewall(gomock.Any(), gomock.Any(), "invocationNamespace").Return(nil, assert.AnError)
	ctr2.EXPECT().State(gomock.Any())
	ctr2.EXPECT().Remove(gomock.Any())

	_, _, err = pool.Get(context.Background(), "invocationNamespace", fnMeta, key.Kind, key.Memory)
	require.ErrorIs(t, err, assert.AnError)
}

func TestContainerPoolSetsUpFirewallForWarm(t *testing.T) {
	t.Parallel()

	fn1Meta := protocol.ActivationMessageFunction{
		Namespace: "test1",
		Name:      "func",
		Revision:  "abc",
	}

	ctrl := gomock.NewController(t)
	factory := NewMockContainerFactory(ctrl)
	firewall := NewMockFirewall(ctrl)
	pool := NewContainerPool(zerolog.Nop(), 4*units.Gigabyte, testManifest, factory, firewall, 0)

	// Invoking the first function will create a container, with no firewall being setup yet.
	ctr1 := newMockContainer(ctrl)
	factory.EXPECT().CreateContainer(gomock.Any(), testKind, 128*units.Megabyte, "wsk_1_invocationNamespace_func").Return(ctr1, nil)
	firewall.EXPECT().NeedsPerContainerFirewallSetup("invocationNamespace").Return(false)

	fn1Container, isReused, err := pool.Get(context.Background(), "invocationNamespace", fn1Meta, "nodejs:foo", 128*units.Megabyte)
	require.NoError(t, err)
	require.False(t, isReused)

	// Returning it will add it to the pool.
	ctr1.EXPECT().PauseEventually(pauseGrace)
	pool.Put(context.Background(), fn1Container)

	// Invoking it again will return the same container, setting up the firewall this time round.
	ctr1.EXPECT().UnpauseIfNeeded(gomock.Any())
	firewall.EXPECT().NeedsPerContainerFirewallSetup("invocationNamespace").Return(true)
	var removeCalled int
	firewall.EXPECT().SetupPerContainerFirewall(gomock.Any(), gomock.Any(), "invocationNamespace").Return(func(context.Context) error {
		removeCalled++
		return nil
	}, nil)

	fn1ContainerSame, isReused, err := pool.Get(context.Background(), "invocationNamespace", fn1Meta, "nodejs:foo", 128*units.Megabyte)
	require.NoError(t, err)
	require.Equal(t, fn1Container, fn1ContainerSame)
	require.True(t, isReused)

	// Returning it will add it to the pool.
	ctr1.EXPECT().PauseEventually(pauseGrace)
	pool.Put(context.Background(), fn1Container)

	// Invoking it again but with the need for a per-container-firewall removed will clean the firewall up.
	ctr1.EXPECT().UnpauseIfNeeded(gomock.Any())
	firewall.EXPECT().NeedsPerContainerFirewallSetup("invocationNamespace").Return(false)

	fn1ContainerSame, isReused, err = pool.Get(context.Background(), "invocationNamespace", fn1Meta, "nodejs:foo", 128*units.Megabyte)
	require.NoError(t, err)
	require.Equal(t, fn1Container, fn1ContainerSame)
	require.True(t, isReused)
	require.Equal(t, 1, removeCalled)

	// Destroying the container does not clean the firewall up again..
	ctr1.EXPECT().Remove(gomock.Any())
	err = pool.PutToDestroy(context.Background(), fn1ContainerSame)
	require.NoError(t, err)
	require.Equal(t, 1, removeCalled)
}

func newMockContainer(ctrl *gomock.Controller) *MockContainer {
	ctr := NewMockContainer(ctrl)
	// ID and IP can be called for logging.
	ctr.EXPECT().ID().Return("").AnyTimes()
	ctr.EXPECT().IP().Return("").AnyTimes()
	return ctr
}

type noopFirewall struct {
	Firewall
}

func (f *noopFirewall) NeedsPerContainerFirewallSetup(_ string) bool {
	return false
}

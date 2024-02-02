package firewall

import (
	"context"
	_ "embed"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	corev1listers "k8s.io/client-go/listers/core/v1"
)

var (
	// The baseline fixtures represent the state on a fresh node and how we want it to be reconciled.
	//go:embed testdata/baseline/in
	baselineIn []byte
	//go:embed testdata/baseline/want
	baselineWant []byte

	// The broken fixtures represent the state on a node with a daemon restart, which breaks the
	// ordering of the rules and how we want it to be reconciled.
	//go:embed testdata/broken/in
	brokenIn []byte
	//go:embed testdata/broken/want
	brokenWant []byte
)

func TestReconcile(t *testing.T) {
	nodes := []*corev1.Node{{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node1",
		},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{{
				Type:    corev1.NodeExternalIP,
				Address: "node1ExternalIP",
			}, {
				Type:    corev1.NodeInternalIP,
				Address: "node1InternalIP", // this is ignored.
			}},
		},
	}, {
		ObjectMeta: metav1.ObjectMeta{
			Name: "node2",
		},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{{
				Type:    corev1.NodeExternalIP,
				Address: "node2ExternalIP",
			}, {
				Type:    corev1.NodeInternalIP,
				Address: "node2InternalIP", // this is ignored.
			}},
		},
	}}

	// All 3 IPs must be present.
	kubeAPIEndpoints := &corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: kubeAPIService.Namespace,
			Name:      kubeAPIService.Name,
		},
		Subsets: []corev1.EndpointSubset{{
			Addresses: []corev1.EndpointAddress{{IP: "kubeAPIIP1"}, {IP: "kubeAPIIP2"}},
		}, {
			Addresses: []corev1.EndpointAddress{{IP: "kubeAPIIP3"}},
		}},
	}

	coreDNSService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: coreDNSService.Namespace,
			Name:      coreDNSService.Name,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "coreDNSClusterIP",
		},
	}
	// All 3 IPs must be present.
	coreDNSEndpoints := &corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: coreDNSService.Namespace,
			Name:      coreDNSService.Name,
		},
		Subsets: []corev1.EndpointSubset{{
			Addresses: []corev1.EndpointAddress{{IP: "coreDNSPodIP1"}, {IP: "coreDNSPodIP2"}},
		}, {
			Addresses: []corev1.EndpointAddress{{IP: "coreDNSPodIP3"}},
		}},
	}

	tests := []struct {
		name            string
		iptables        *fakeIPTables
		nodeLister      *fakeNodeLister
		serviceLister   *fakeServiceLister
		endpointsLister *fakeEndpointsLister
		wantWritten     []byte
		wantError       bool
	}{{
		name:       "succeeds reconciling baseline state",
		iptables:   &fakeIPTables{current: baselineIn},
		nodeLister: &fakeNodeLister{nodes: nodes},
		serviceLister: &fakeServiceLister{services: map[types.NamespacedName]*corev1.Service{
			{Namespace: coreDNSService.Namespace, Name: coreDNSService.Name}: coreDNSService,
		}},
		endpointsLister: &fakeEndpointsLister{endpoints: map[types.NamespacedName]*corev1.Endpoints{
			{Namespace: kubeAPIEndpoints.Namespace, Name: kubeAPIEndpoints.Name}: kubeAPIEndpoints,
			{Namespace: coreDNSEndpoints.Namespace, Name: coreDNSEndpoints.Name}: coreDNSEndpoints,
		}},
		wantWritten: baselineWant,
	}, {
		name:       "succeeds reconciling broken state",
		iptables:   &fakeIPTables{current: brokenIn},
		nodeLister: &fakeNodeLister{nodes: nodes},
		serviceLister: &fakeServiceLister{services: map[types.NamespacedName]*corev1.Service{
			{Namespace: coreDNSService.Namespace, Name: coreDNSService.Name}: coreDNSService,
		}},
		endpointsLister: &fakeEndpointsLister{endpoints: map[types.NamespacedName]*corev1.Endpoints{
			{Namespace: kubeAPIEndpoints.Namespace, Name: kubeAPIEndpoints.Name}: kubeAPIEndpoints,
			{Namespace: coreDNSEndpoints.Namespace, Name: coreDNSEndpoints.Name}: coreDNSEndpoints,
		}},
		wantWritten: brokenWant,
	}, {
		name:       "fails to list nodes",
		nodeLister: &fakeNodeLister{err: assert.AnError},
		wantError:  true,
	}, {
		name:            "fails to get kubernetes API endpoints",
		nodeLister:      &fakeNodeLister{nodes: nodes},
		endpointsLister: &fakeEndpointsLister{},
		wantError:       true,
	}, {
		name:       "fails to get coreDNS service",
		nodeLister: &fakeNodeLister{nodes: nodes},
		endpointsLister: &fakeEndpointsLister{endpoints: map[types.NamespacedName]*corev1.Endpoints{
			{Namespace: kubeAPIEndpoints.Namespace, Name: kubeAPIEndpoints.Name}: kubeAPIEndpoints,
		}},
		serviceLister: &fakeServiceLister{},
		wantError:     true,
	}, {
		name:       "fails to get coreDNS endpoints",
		nodeLister: &fakeNodeLister{nodes: nodes},
		endpointsLister: &fakeEndpointsLister{endpoints: map[types.NamespacedName]*corev1.Endpoints{
			{Namespace: kubeAPIEndpoints.Namespace, Name: kubeAPIEndpoints.Name}: kubeAPIEndpoints,
			// Lacks coreDNS Endpoints.
		}},
		serviceLister: &fakeServiceLister{services: map[types.NamespacedName]*corev1.Service{
			{Namespace: coreDNSService.Namespace, Name: coreDNSService.Name}: coreDNSService,
		}},
		wantError: true,
	}, {
		name:       "fails to get iptables state",
		iptables:   &fakeIPTables{fetchErr: assert.AnError},
		nodeLister: &fakeNodeLister{nodes: nodes},
		serviceLister: &fakeServiceLister{services: map[types.NamespacedName]*corev1.Service{
			{Namespace: coreDNSService.Namespace, Name: coreDNSService.Name}: coreDNSService,
		}},
		endpointsLister: &fakeEndpointsLister{endpoints: map[types.NamespacedName]*corev1.Endpoints{
			{Namespace: kubeAPIEndpoints.Namespace, Name: kubeAPIEndpoints.Name}: kubeAPIEndpoints,
			{Namespace: coreDNSEndpoints.Namespace, Name: coreDNSEndpoints.Name}: coreDNSEndpoints,
		}},
		wantError: true,
	}, {
		name:       "fails to write iptables state",
		iptables:   &fakeIPTables{storeErr: assert.AnError},
		nodeLister: &fakeNodeLister{nodes: nodes},
		serviceLister: &fakeServiceLister{services: map[types.NamespacedName]*corev1.Service{
			{Namespace: coreDNSService.Namespace, Name: coreDNSService.Name}: coreDNSService,
		}},
		endpointsLister: &fakeEndpointsLister{endpoints: map[types.NamespacedName]*corev1.Endpoints{
			{Namespace: kubeAPIEndpoints.Namespace, Name: kubeAPIEndpoints.Name}: kubeAPIEndpoints,
			{Namespace: coreDNSEndpoints.Namespace, Name: coreDNSEndpoints.Name}: coreDNSEndpoints,
		}},
		wantError: true,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Reconciler{
				Logger: zerolog.New(zerolog.NewTestWriter(t)),

				ContainerSubnet: "172.18.0.0/16",
				ContainerBridge: "functions0",

				IPTables:        tt.iptables,
				NodeLister:      tt.nodeLister,
				ServiceLister:   tt.serviceLister,
				EndpointsLister: tt.endpointsLister,
			}
			err := r.Reconcile(context.Background())
			if tt.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, string(tt.wantWritten), string(tt.iptables.written))
			}
		})
	}
}

type fakeNodeLister struct {
	corev1listers.NodeLister

	nodes []*corev1.Node
	err   error
}

func (f *fakeNodeLister) List(labels.Selector) ([]*corev1.Node, error) {
	return f.nodes, f.err
}

type fakeServiceLister struct {
	corev1listers.ServiceNamespaceLister

	namespace string
	services  map[types.NamespacedName]*corev1.Service
}

func (f *fakeServiceLister) Services(ns string) corev1listers.ServiceNamespaceLister {
	return &fakeServiceLister{
		namespace: ns,
		services:  f.services,
	}
}

func (f *fakeServiceLister) Get(name string) (*corev1.Service, error) {
	svc, ok := f.services[types.NamespacedName{Namespace: f.namespace, Name: name}]
	if !ok {
		return nil, assert.AnError
	}
	return svc, nil
}

type fakeEndpointsLister struct {
	corev1listers.EndpointsNamespaceLister

	namespace string
	endpoints map[types.NamespacedName]*corev1.Endpoints
}

func (f *fakeEndpointsLister) Endpoints(ns string) corev1listers.EndpointsNamespaceLister {
	return &fakeEndpointsLister{
		namespace: ns,
		endpoints: f.endpoints,
	}
}

func (f *fakeEndpointsLister) Get(name string) (*corev1.Endpoints, error) {
	svc, ok := f.endpoints[types.NamespacedName{Namespace: f.namespace, Name: name}]
	if !ok {
		return nil, assert.AnError
	}
	return svc, nil
}

type fakeIPTables struct {
	current  []byte
	fetchErr error

	written  []byte
	storeErr error

	execCalls []string
	execFunc  func([]string) error
}

func (f *fakeIPTables) GetState(context.Context) ([]byte, error) {
	return f.current, f.fetchErr
}

func (f *fakeIPTables) WriteState(_ context.Context, state []byte) error {
	f.written = state
	return f.storeErr
}

func (f *fakeIPTables) Exec(_ context.Context, args []string) error {
	f.execCalls = append(f.execCalls, strings.Join(args, " "))
	if f.execFunc != nil {
		return f.execFunc(args)
	}
	return nil
}

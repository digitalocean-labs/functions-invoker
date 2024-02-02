package firewall

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestFilterAddDelete(t *testing.T) {
	tests := []struct {
		name       string
		in         runtime.Object
		wantCalled bool
	}{{
		name:       "coreDNS service passes",
		in:         svc(coreDNSService.Namespace, coreDNSService.Name, ""),
		wantCalled: true,
	}, {
		name:       "any other service doesn't pass",
		in:         svc("foo", "bar", ""),
		wantCalled: false,
	}, {
		name:       "coreDNS endpoints passes",
		in:         eps(coreDNSService.Namespace, coreDNSService.Name, nil),
		wantCalled: true,
	}, {
		name:       "kubernetes API endpoints passes",
		in:         eps(kubeAPIService.Namespace, kubeAPIService.Name, nil),
		wantCalled: true,
	}, {
		name:       "any other endpoints don't pass",
		in:         eps("foo", "bar", nil),
		wantCalled: false,
	}, {
		name:       "any node passes",
		in:         node("foo", ""),
		wantCalled: true,
	}}

	for _, tt := range tests {
		t.Run("OnAdd "+tt.name, func(t *testing.T) {
			var called bool
			filter := FilteredEventHandler(func() { called = true })
			filter.OnAdd(tt.in, false)
			require.Equal(t, tt.wantCalled, called)
		})

		t.Run("OnDelete "+tt.name, func(t *testing.T) {
			var called bool
			filter := FilteredEventHandler(func() { called = true })
			filter.OnDelete(tt.in)
			require.Equal(t, tt.wantCalled, called)
		})
	}
}

func TestFilterUpdate(t *testing.T) {
	tests := []struct {
		name       string
		old        runtime.Object
		new        runtime.Object
		wantCalled bool
	}{{
		name:       "coreDNS service with changed IP passes",
		old:        svc(coreDNSService.Namespace, coreDNSService.Name, "testIP"),
		new:        svc(coreDNSService.Namespace, coreDNSService.Name, "changedIP"),
		wantCalled: true,
	}, {
		name:       "coreDNS service with same IP doesn't pass",
		old:        svc(coreDNSService.Namespace, coreDNSService.Name, "testIP"),
		new:        svc(coreDNSService.Namespace, coreDNSService.Name, "testIP"),
		wantCalled: false,
	}, {
		name:       "any other service doesn't pass, even with changes",
		old:        svc("foo", "bar", "testIP"),
		new:        svc("foo", "bar", "changedIP"),
		wantCalled: false,
	}, {
		name:       "coreDNS endpoints with changed IPs passes",
		old:        eps(coreDNSService.Namespace, coreDNSService.Name, []string{"testIP"}),
		new:        eps(coreDNSService.Namespace, coreDNSService.Name, []string{"testIP", "testIP2"}),
		wantCalled: true,
	}, {
		name:       "coreDNS endpoints with same IPs doesn't pass",
		old:        eps(coreDNSService.Namespace, coreDNSService.Name, []string{"testIP"}),
		new:        eps(coreDNSService.Namespace, coreDNSService.Name, []string{"testIP"}),
		wantCalled: false,
	}, {
		name:       "kubernetes API endpoints with changed IPs passes",
		old:        eps(kubeAPIService.Namespace, kubeAPIService.Name, []string{"testIP"}),
		new:        eps(kubeAPIService.Namespace, kubeAPIService.Name, []string{"testIP", "testIP2"}),
		wantCalled: true,
	}, {
		name:       "kubernetes API endpoints with same IPs doesn't pass",
		old:        eps(kubeAPIService.Namespace, kubeAPIService.Name, []string{"testIP"}),
		new:        eps(kubeAPIService.Namespace, kubeAPIService.Name, []string{"testIP"}),
		wantCalled: false,
	}, {
		name:       "any other endpoints doesn't pass, even with changes",
		old:        eps("foo", "bar", []string{"testIP"}),
		new:        eps("foo", "bar", []string{"testIP", "testIP2"}),
		wantCalled: false,
	}, {
		name:       "any node with changed IP passes",
		old:        node("foo", "testIP"),
		new:        node("foo", "changedIP"),
		wantCalled: true,
	}, {
		name:       "any node with same IP doesn't pass",
		old:        node("foo", "testIP"),
		new:        node("foo", "testIP"),
		wantCalled: false,
	}, {
		name:       "broken type mix doesn't pass for node",
		old:        node("bar", "testIP"),
		new:        svc("foo", "bar", "testIP"),
		wantCalled: false,
	}, {
		name:       "broken type mix doesn't pass for service",
		old:        svc("foo", "bar", "testIP"),
		new:        node("foo", "testIP"),
		wantCalled: false,
	}, {
		name:       "broken type mix doesn't pass for endpoints",
		old:        eps("foo", "bar", []string{"testIP"}),
		new:        svc("foo", "bar", "testIP"),
		wantCalled: false,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var called bool
			filter := FilteredEventHandler(func() { called = true })
			filter.OnUpdate(tt.old, tt.new)
			require.Equal(t, tt.wantCalled, called)
		})
	}
}

func svc(ns, name, clusterIP string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: clusterIP,
		},
	}
}

func eps(ns, name string, ips []string) *corev1.Endpoints {
	addresses := make([]corev1.EndpointAddress, 0, len(ips))
	for _, ip := range ips {
		addresses = append(addresses, corev1.EndpointAddress{IP: ip})
	}
	return &corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
		},
		Subsets: []corev1.EndpointSubset{{
			Addresses: addresses,
		}},
	}
}

func node(name, externalIP string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{{
				Type:    corev1.NodeExternalIP,
				Address: externalIP,
			}},
		},
	}
}

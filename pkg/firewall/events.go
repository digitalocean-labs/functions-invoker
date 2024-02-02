package firewall

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/client-go/tools/cache"
)

// FilteredEventHandler is an event handler for informers that heavily filters down to only the
// updates relevant for firewalls to avoid a lot of churn.
func FilteredEventHandler(f func()) cache.ResourceEventHandlerDetailedFuncs {
	return cache.ResourceEventHandlerDetailedFuncs{
		AddFunc: func(obj interface{}, isInInitialList bool) {
			if isInInitialList {
				// We skip the initial list as we force a reconciliation on startup anyway.
				return
			}

			switch typedObj := obj.(type) {
			case *corev1.Service:
				if isRelevantService(typedObj) {
					f()
				}
			case *corev1.Endpoints:
				if isRelevantEndpoints(typedObj) {
					f()
				}
			default:
				f()
			}
		},
		UpdateFunc: func(old, changed interface{}) {
			switch oldObj := old.(type) {
			case *corev1.Node:
				newObj, ok := changed.(*corev1.Node)
				if !ok {
					return
				}
				if !equality.Semantic.DeepEqual(oldObj.Status.Addresses, newObj.Status.Addresses) {
					f()
				}
			case *corev1.Service:
				newObj, ok := changed.(*corev1.Service)
				if !ok {
					return
				}
				if isRelevantService(newObj) && oldObj.Spec.ClusterIP != newObj.Spec.ClusterIP {
					f()
				}
			case *corev1.Endpoints:
				newObj, ok := changed.(*corev1.Endpoints)
				if !ok {
					return
				}
				if isRelevantEndpoints(newObj) && !equality.Semantic.DeepEqual(oldObj.Subsets, newObj.Subsets) {
					f()
				}
			}
		},
		DeleteFunc: func(obj interface{}) {
			switch typedObj := obj.(type) {
			case *corev1.Service:
				if isRelevantService(typedObj) {
					f()
				}
			case *corev1.Endpoints:
				if isRelevantEndpoints(typedObj) {
					f()
				}
			default:
				f()
			}
		},
	}
}

func isRelevantService(svc *corev1.Service) bool {
	return svc.Namespace == coreDNSService.Namespace && svc.Name == coreDNSService.Name
}

func isRelevantEndpoints(eps *corev1.Endpoints) bool {
	return eps.Namespace == coreDNSService.Namespace && eps.Name == coreDNSService.Name ||
		eps.Namespace == kubeAPIService.Namespace && eps.Name == kubeAPIService.Name
}

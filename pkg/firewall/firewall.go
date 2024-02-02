package firewall

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

const (

	// The following represent desired chains and rules that we expect to be part of the setup.
	firewallChain           = ":WHISK-FIREWALL - [0:0]"
	perContainerChain       = ":PER-CONTAINER-FIREWALL - [0:0]"
	perContainerForwardRule = "-A FORWARD -j PER-CONTAINER-FIREWALL"
	firewallForwardRule     = "-A FORWARD -j WHISK-FIREWALL"
	firewallInputRule       = "-A INPUT -j WHISK-FIREWALL"
)

var (
	// coreDNSService is the service that's used to resolve DNS addresses for the function containers.
	// It has to be reachable.
	// We're not reaching out to this, we just need to know about it to poke the hole.
	coreDNSService = types.NamespacedName{Namespace: "serverless-coredns", Name: "serverless-coredns"}

	// kubeAPIService is a "magic" service in that a publicly available K8s API Server is spoofed in.
	// We don't want Functions to reach that endpoint ever.
	// We're not reaching out to this, we just need to know about it reject traffic explicitly.
	kubeAPIService = types.NamespacedName{Namespace: "default", Name: "kubernetes"}
)

// IPTables is an interface that captures the iptables operations.
type IPTables interface {
	// GetState fetches the current state of all iptables.
	GetState(context.Context) ([]byte, error)
	// WriteState stores the holistic iptables state.
	WriteState(context.Context, []byte) error
	// Exec runs iptables with the given arguments.
	Exec(context.Context, []string) error
}

type Reconciler struct {
	Logger zerolog.Logger

	// ContainerSubnet is the subnet in which the containers run.
	ContainerSubnet string
	// ContainerBridge is the bridge network on which the containers run.
	ContainerBridge string

	IPTables        IPTables
	NodeLister      corev1listers.NodeLister
	ServiceLister   corev1listers.ServiceLister
	EndpointsLister corev1listers.EndpointsLister
	TSLister        cache.GenericNamespaceLister

	// mux prevents the general firewall management and the per-container-firewall setup to run concurrently.
	mux sync.Mutex
}

// Reconcile idempotently brings the firewall to our desired state.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	// Fetch all nodes to be able to block their IPs.
	nodes, err := r.NodeLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("failed to get nodes: %w", err)
	}

	// Fetch the kubeAPI Endpoints to block it.
	kubeAPIEndpoints, err := r.EndpointsLister.Endpoints(kubeAPIService.Namespace).Get(kubeAPIService.Name)
	if err != nil {
		return fmt.Errorf("failed to get default service endpoints: %w", err)
	}

	// Fetch the CoreDNS service and endpoints to allow them.
	coreDNSService, err := r.ServiceLister.Services(coreDNSService.Namespace).Get(coreDNSService.Name)
	if err != nil {
		return fmt.Errorf("failed to get CoreDNS service: %w", err)
	}
	coreDNSEndpoints, err := r.EndpointsLister.Endpoints(coreDNSService.Namespace).Get(coreDNSService.Name)
	if err != nil {
		return fmt.Errorf("failed to get CoreDNS service endpoints: %w", err)
	}

	r.mux.Lock()
	defer r.mux.Unlock()

	// Fetch the current iptables state.
	current, err := r.IPTables.GetState(ctx)
	if err != nil {
		return fmt.Errorf("failed to get current iptables: %w", err)
	}

	// Generate the desired state.
	newTable := r.reconcile(current, nodes, kubeAPIEndpoints, coreDNSService, coreDNSEndpoints)

	// Apply the desired state.
	if err := r.IPTables.WriteState(ctx, []byte(newTable)); err != nil {
		return fmt.Errorf("failed to write iptables: %w", err)
	}
	return nil
}

func (r *Reconciler) reconcile(current []byte, nodes []*corev1.Node, kubeAPIEndpoints *corev1.Endpoints, coreDNSService *corev1.Service, coreDNSEndpoints *corev1.Endpoints) string {
	// "Parse" the output and sanitize (read: delete) rules that we're going to readd in a consistent
	// spot.
	var rules []string
	scanner := bufio.NewScanner(bytes.NewReader(current))

	var (
		currentChain              string
		ruleIndexInChain          int
		containsPerContainerChain bool
		containsFirewallChain     bool
	)
	for scanner.Scan() {
		line := scanner.Text()
		// We drop everything but chain and rule lines. Notably, this drops comments and the final COMMIT.
		if !strings.HasPrefix(line, "*") && !strings.HasPrefix(line, ":") && !strings.HasPrefix(line, "-") {
			continue
		}

		switch {
		case line == firewallChain:
			containsFirewallChain = true
		case line == perContainerChain:
			containsPerContainerChain = true
		case strings.HasPrefix(line, "-A"):
			// The following block only exists to gather insights into how often we need to "fixup"
			// the firewalls. It checks if our rules are in the correct positions of the respective
			// iptables chains.
			// We can consider removing this block if we find that the iptables state is actually
			// stable after an initial fixup on restart.
			ruleParts := strings.SplitN(line, " ", 3)
			if len(ruleParts) > 1 {
				chain := ruleParts[1]
				if currentChain != chain {
					// We assume that the input has all the chain rules ordered by chain, so
					// whenever we see a new chain name, we reset chain + counter.
					currentChain = chain
					ruleIndexInChain = 0
				} else {
					ruleIndexInChain++
				}

				if currentChain == "FORWARD" {
					if ruleIndexInChain == 0 && !strings.HasPrefix(line, perContainerForwardRule) {
						r.Logger.Warn().Msg("PER-CONTAINER-FIREWALL is not the first rule in the FORWARD chain")
					} else if ruleIndexInChain == 1 && !strings.HasPrefix(line, firewallForwardRule) {
						r.Logger.Warn().Msg("WHISK-FIREWALL is not the second rule in the FORWARD chain")
					}
				} else if currentChain == "INPUT" {
					if ruleIndexInChain == 0 && !strings.HasPrefix(line, firewallInputRule) {
						r.Logger.Warn().Msg("WHISK-FIREWALL is not the first rule in the INPUT chain")
					}
				}
			} else {
				r.Logger.Warn().Str("rule", line).Msg("Unexpected malformed rule")
			}

			// Remove all WHISK-FIREWALL rules to build things up freshly.
			if strings.HasPrefix(line, "-A WHISK-FIREWALL") ||
				// Also remove WHISK-FIREWALL and PER-CONTAINER-FIREWALL FORWARD chain commands to ensure
				// we put them in as the first rules.
				strings.HasPrefix(line, firewallForwardRule) ||
				strings.HasPrefix(line, perContainerForwardRule) ||
				// Also remove the WHISK-FIREWALL INPUT chain rule to ensure we can put it in first.
				strings.HasPrefix(line, firewallInputRule) {
				continue
			}
		}

		rules = append(rules, line)
	}

	// Create the WHISK-FIREWALL and PER-CONTAINER-FIREWALL chains if they don't exist yet.
	if !containsFirewallChain {
		r.Logger.Warn().Msg("WHISK-FIREWALL chain does not exist")
		rules = append(rules, ":WHISK-FIREWALL - [0:0]")
	}
	if !containsPerContainerChain {
		r.Logger.Warn().Msg("PER-CONTAINER-FIREWALL chain does not exist")
		rules = append(rules, ":PER-CONTAINER-FIREWALL - [0:0]")
	}

	// Add PER-CONTAINER-FIREWALL as first rule in FORWARD chain followed by WHISK-FIREWALL.
	// They're flipped because -I causes the rule to be inserted at the front.
	rules = append(rules, "-I FORWARD -j WHISK-FIREWALL")
	rules = append(rules, "-I FORWARD -j PER-CONTAINER-FIREWALL")

	// Ensure WHISK-FIREWALL is the first rule in the INPUT chain.
	rules = append(rules, "-I INPUT -j WHISK-FIREWALL")

	// Allow packets from the container to flow back to the invoker.
	rules = append(rules, "-A WHISK-FIREWALL -m conntrack --ctstate ESTABLISHED -j ACCEPT")

	// Reject all external node IPs.
	for _, node := range nodes {
		for _, address := range node.Status.Addresses {
			if address.Type != corev1.NodeExternalIP {
				// We only pick ExternalIP as that's the public IP of the node and can be reached
				// by a function container hosted on the node. InternalIP is the VPC IP that will
				// not be reachable because of the blanket 10/8 reject rule that we already have
				// in the WHISK-FIREWALL chain.
				continue
			}
			rules = append(rules, r.serviceRule(address.Address, "REJECT", "node: "+node.Name))
		}
	}

	// Reject the underlying IP of the default/kubernetes service
	for _, subset := range kubeAPIEndpoints.Subsets {
		for _, address := range subset.Addresses {
			rules = append(rules, r.serviceRule(address.IP, "REJECT", "kube api service"))
		}
	}

	// Poke a hole for the coredns service/pods.
	rules = append(rules, r.serviceRule(coreDNSService.Spec.ClusterIP, "ACCEPT", "CoreDNS service"))
	for _, subset := range coreDNSEndpoints.Subsets {
		for _, address := range subset.Addresses {
			rules = append(rules, r.serviceRule(address.IP, "ACCEPT", "CoreDNS pod"))
		}
	}

	// Blanket disallow anything in the 10 dot network. This catches cluster services, pod IPs
	// and internal IPs of nodes.
	rules = append(rules, r.serviceRule("10.0.0.0/8", "REJECT", "cluster services"))

	// Reject all traffic to the bridge gateway. This catches node-local services.
	rules = append(rules, r.serviceRule(r.ContainerSubnet, "REJECT", "bridge gateway"))

	// Reject link-local traffic coming from the docker container network.
	rules = append(rules, fmt.Sprintf(`-A WHISK-FIREWALL -i %s ! -o %s -s %s -d 169.254.0.0/16 -j REJECT -m comment --comment "link local subnet"`, r.ContainerBridge, r.ContainerBridge, r.ContainerSubnet))

	// Close the WHISK-FIREWALL rules.
	rules = append(rules, "-A WHISK-FIREWALL -j RETURN")
	rules = append(rules, "COMMIT")

	// The closing newline is important, otherwise iptables-restore will fail.
	return strings.Join(rules, "\n") + "\n"
}

// NeedsPerContainerFirewallSetup returns whether or not a firewall has to be setup for the given namespace.
func (r *Reconciler) NeedsPerContainerFirewallSetup(_ string) bool {
	// Not implemented. Use-case specific code lives here.
	return false
}

// SetupPerContainerFirewall sets up firewall rules for the given container IP.
// The returned function must be called to clean up once the container is deleted.
func (r *Reconciler) SetupPerContainerFirewall(_ context.Context, _ string, _ string) (func(context.Context) error, error) {
	// Not implemented. Use-case specific code lives here.
	return func(ctx context.Context) error { return nil }, nil
}

func (r *Reconciler) serviceRule(destination string, action string, comment string) string {
	return fmt.Sprintf(`-A WHISK-FIREWALL -i %s ! -o %s -d %s -j %s -m comment --comment "%s"`, r.ContainerBridge, r.ContainerBridge, destination, action, comment)
}

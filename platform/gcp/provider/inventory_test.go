package gcp

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestTheServiceInventoryMatchesWhatServiceOfSends(t *testing.T) {
	t.Parallel()

	for _, compute := range []providerkit.Compute{providerkit.ComputeServerless, providerkit.ComputeContainer} {
		sent, err := serviceOf(serving{service: "svc", image: "img", compute: compute, health: "/healthz", ingress: ingressLoadBalancer})
		if err != nil {
			t.Fatal(err)
		}
		properties := serviceProperties(compute, ingressLoadBalancer)
		if properties["ingress"] != sent.Ingress {
			t.Errorf("%s: the inventory reads ingress %v, serviceOf sends %v", compute, properties["ingress"], sent.Ingress)
		}
		template := properties["template"].(map[string]any)
		scaling := template["scaling"].(map[string]any)
		if int64(scaling["min_instance_count"].(int)) != sent.Template.Scaling.MinInstanceCount {
			t.Errorf("%s: the inventory reads min instances %v, serviceOf sends %d", compute, scaling["min_instance_count"], sent.Template.Scaling.MinInstanceCount)
		}
		resources := template["containers"].([]any)[0].(map[string]any)["resources"].(map[string]any)
		limits := resources["limits"].(map[string]any)
		if resources["cpu_idle"] != sent.Template.Containers[0].Resources.CpuIdle {
			t.Errorf("%s: the inventory reads cpu_idle %v, serviceOf sends %v", compute, resources["cpu_idle"], sent.Template.Containers[0].Resources.CpuIdle)
		}
		if limits["cpu"] != sent.Template.Containers[0].Resources.Limits["cpu"] || limits["memory"] != sent.Template.Containers[0].Resources.Limits["memory"] {
			t.Errorf("%s: the inventory reads limits %v, serviceOf sends %v", compute, limits, sent.Template.Containers[0].Resources.Limits)
		}
	}
}

func TestEveryBootstrapItemHasAnInventoryType(t *testing.T) {
	t.Parallel()

	names := Names{namespace: "ocel", project: "acme-prod"}
	for _, class := range []providerkit.Class{providerkit.ClassProduction, providerkit.ClassPreview} {
		for _, item := range bootstrapItems(names, class, false) {
			if _, named := itemTypes[item.Kind]; !named {
				t.Errorf("%s stands up a %s the inventory has no type for", class, item.ID())
			}
		}
	}
}

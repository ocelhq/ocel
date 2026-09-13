package gcp

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestTheServiceShapeMatchesWhatServiceOfSends(t *testing.T) {
	t.Parallel()

	for _, compute := range []providerkit.Compute{providerkit.ComputeServerless, providerkit.ComputeContainer} {
		sent, err := serviceOf(serving{service: "svc", image: "img", compute: compute, health: "/healthz"})
		if err != nil {
			t.Fatal(err)
		}
		shaped := serviceProperties(compute)["template"].(map[string]any)
		scaling := shaped["scaling"].(map[string]any)
		if int64(scaling["min_instance_count"].(int)) != sent.Template.Scaling.MinInstanceCount {
			t.Errorf("%s: shaped min instances %v, serviceOf sends %d", compute, scaling["min_instance_count"], sent.Template.Scaling.MinInstanceCount)
		}
		resources := shaped["containers"].([]any)[0].(map[string]any)["resources"].(map[string]any)
		limits := resources["limits"].(map[string]any)
		if resources["cpu_idle"] != sent.Template.Containers[0].Resources.CpuIdle {
			t.Errorf("%s: shaped cpu_idle %v, serviceOf sends %v", compute, resources["cpu_idle"], sent.Template.Containers[0].Resources.CpuIdle)
		}
		if limits["cpu"] != sent.Template.Containers[0].Resources.Limits["cpu"] || limits["memory"] != sent.Template.Containers[0].Resources.Limits["memory"] {
			t.Errorf("%s: shaped limits %v, serviceOf sends %v", compute, limits, sent.Template.Containers[0].Resources.Limits)
		}
	}
}

func TestEveryBootstrapItemHasAShape(t *testing.T) {
	t.Parallel()

	names := Names{namespace: "ocel", project: "acme-prod"}
	for _, class := range []providerkit.Class{providerkit.ClassProduction, providerkit.ClassPreview} {
		for _, item := range bootstrapItems(names, class, false) {
			if _, shaped := itemTypes[item.Kind]; !shaped {
				t.Errorf("%s stands up a %s the shape has no name for", class, item.ID())
			}
		}
	}
}

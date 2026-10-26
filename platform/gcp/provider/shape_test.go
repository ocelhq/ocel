package gcp

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/envsource"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

func TestTheServiceShapeMatchesWhatServiceOfSends(t *testing.T) {
	t.Parallel()

	for compute, instances := range map[provider.Compute]provider.Instances{provider.ComputeServerless: {}, provider.ComputeContainer: {Min: 2, Max: 2}} {
		sent, err := run.ServiceOf(run.Serving{Service: "svc", Image: "img", Compute: compute, Health: "/healthz", Ingress: run.IngressLoadBalancer, Instances: instances})
		if err != nil {
			t.Fatal(err)
		}
		properties := run.PricedProperties(compute == provider.ComputeServerless, instances.Min, run.RevisionMemory, run.IngressLoadBalancer)
		if properties["ingress"] != sent.Ingress {
			t.Errorf("%s: shaped ingress %v, serviceOf sends %v", compute, properties["ingress"], sent.Ingress)
		}
		shaped := properties["template"].(map[string]any)
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

func TestTheEnvSourceSyncShapeMatchesWhatTheBootstrapDeploys(t *testing.T) {
	t.Parallel()

	b := composed(bootstrap.Engine{ServiceStates: &bootstrap.ServiceStates{}, Clients: &gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1"}})
	sent, err := run.ServiceOf(envsource.SyncServing(b, environment.TierProduction))
	if err != nil {
		t.Fatal(err)
	}
	properties := envsource.ServiceProperties("europe-west1")
	if properties["ingress"] != sent.Ingress {
		t.Errorf("shaped ingress %v, the bootstrap sends %v", properties["ingress"], sent.Ingress)
	}
	shaped := properties["template"].(map[string]any)
	scaling := shaped["scaling"].(map[string]any)
	if int64(scaling["min_instance_count"].(int)) != sent.Template.Scaling.MinInstanceCount ||
		int64(scaling["max_instance_count"].(int)) != sent.Template.Scaling.MaxInstanceCount {
		t.Errorf("shaped scaling %v, the bootstrap sends %+v", scaling, sent.Template.Scaling)
	}
	resources := shaped["containers"].([]any)[0].(map[string]any)["resources"].(map[string]any)
	limits := resources["limits"].(map[string]any)
	if resources["cpu_idle"] != sent.Template.Containers[0].Resources.CpuIdle {
		t.Errorf("shaped cpu_idle %v, the bootstrap sends %v", resources["cpu_idle"], sent.Template.Containers[0].Resources.CpuIdle)
	}
	if limits["cpu"] != sent.Template.Containers[0].Resources.Limits["cpu"] || limits["memory"] != sent.Template.Containers[0].Resources.Limits["memory"] {
		t.Errorf("shaped limits %v, the bootstrap sends %v", limits, sent.Template.Containers[0].Resources.Limits)
	}
}

func TestEveryBootstrapItemHasAShape(t *testing.T) {
	t.Parallel()

	names := names.New("ocel", "acme-prod")
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		for _, item := range bootstrap.Resources(names, tier, false) {
			if _, shaped := item.Shape("europe-west1"); !shaped {
				t.Errorf("%s provisions a %s the shape has no name for", tier, item.ID())
			}
		}
	}
}

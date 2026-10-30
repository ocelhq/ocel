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

func TestTheServiceInventoryMatchesWhatServiceOfSends(t *testing.T) {
	t.Parallel()

	for compute, instances := range map[provider.Compute]provider.Instances{provider.ComputeServerless: {}, provider.ComputeContainer: {Min: 2, Max: 2}} {
		sent, err := run.ServiceOf(run.Serving{Service: "svc", Image: "img", Compute: compute, Health: "/healthz", Ingress: run.IngressLoadBalancer, Instances: instances})
		if err != nil {
			t.Fatal(err)
		}
		properties := run.PricedProperties(compute == provider.ComputeServerless, instances.Min, run.RevisionMemory, run.IngressLoadBalancer)
		if properties["ingress"] != sent.Ingress {
			t.Errorf("%s: inventoried ingress %v, serviceOf sends %v", compute, properties["ingress"], sent.Ingress)
		}
		inventoried := properties["template"].(map[string]any)
		scaling := inventoried["scaling"].(map[string]any)
		if int64(scaling["min_instance_count"].(int)) != sent.Template.Scaling.MinInstanceCount {
			t.Errorf("%s: inventoried min instances %v, serviceOf sends %d", compute, scaling["min_instance_count"], sent.Template.Scaling.MinInstanceCount)
		}
		resources := inventoried["containers"].([]any)[0].(map[string]any)["resources"].(map[string]any)
		limits := resources["limits"].(map[string]any)
		if resources["cpu_idle"] != sent.Template.Containers[0].Resources.CpuIdle {
			t.Errorf("%s: inventoried cpu_idle %v, serviceOf sends %v", compute, resources["cpu_idle"], sent.Template.Containers[0].Resources.CpuIdle)
		}
		if limits["cpu"] != sent.Template.Containers[0].Resources.Limits["cpu"] || limits["memory"] != sent.Template.Containers[0].Resources.Limits["memory"] {
			t.Errorf("%s: inventoried limits %v, serviceOf sends %v", compute, limits, sent.Template.Containers[0].Resources.Limits)
		}
	}
}

func TestTheEnvSourceSyncInventoryMatchesWhatTheBootstrapDeploys(t *testing.T) {
	t.Parallel()

	b := composed(bootstrap.Engine{Clients: &gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1"}})
	sent, err := run.ServiceOf(envsource.SyncServing(b, environment.TierProduction))
	if err != nil {
		t.Fatal(err)
	}
	properties := envsource.ServiceProperties("europe-west1")
	if properties["ingress"] != sent.Ingress {
		t.Errorf("inventoried ingress %v, the bootstrap sends %v", properties["ingress"], sent.Ingress)
	}
	inventoried := properties["template"].(map[string]any)
	scaling := inventoried["scaling"].(map[string]any)
	if int64(scaling["min_instance_count"].(int)) != sent.Template.Scaling.MinInstanceCount ||
		int64(scaling["max_instance_count"].(int)) != sent.Template.Scaling.MaxInstanceCount {
		t.Errorf("inventoried scaling %v, the bootstrap sends %+v", scaling, sent.Template.Scaling)
	}
	resources := inventoried["containers"].([]any)[0].(map[string]any)["resources"].(map[string]any)
	limits := resources["limits"].(map[string]any)
	if resources["cpu_idle"] != sent.Template.Containers[0].Resources.CpuIdle {
		t.Errorf("inventoried cpu_idle %v, the bootstrap sends %v", resources["cpu_idle"], sent.Template.Containers[0].Resources.CpuIdle)
	}
	if limits["cpu"] != sent.Template.Containers[0].Resources.Limits["cpu"] || limits["memory"] != sent.Template.Containers[0].Resources.Limits["memory"] {
		t.Errorf("inventoried limits %v, the bootstrap sends %v", limits, sent.Template.Containers[0].Resources.Limits)
	}
}

func TestEveryBootstrapResourceIsPriced(t *testing.T) {
	t.Parallel()

	names := names.New("ocel", "acme-prod")
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		for _, item := range bootstrap.Resources(names, tier, false) {
			if _, priced := item.Priced("europe-west1"); !priced {
				t.Errorf("%s provisions a %s with no type the rate card prices", tier, item.ID())
			}
		}
	}
}

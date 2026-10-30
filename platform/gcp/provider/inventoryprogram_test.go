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
		properties := run.ServiceProperties(compute == provider.ComputeServerless, instances.Min, run.RevisionMemory, run.IngressLoadBalancer)
		if properties["ingress"] != sent.Ingress {
			t.Errorf("%s: lists ingress %v, serviceOf sends %v", compute, properties["ingress"], sent.Ingress)
		}
		listed := properties["template"].(map[string]any)
		scaling := listed["scaling"].(map[string]any)
		if int64(scaling["min_instance_count"].(int)) != sent.Template.Scaling.MinInstanceCount {
			t.Errorf("%s: lists min instances %v, serviceOf sends %d", compute, scaling["min_instance_count"], sent.Template.Scaling.MinInstanceCount)
		}
		resources := listed["containers"].([]any)[0].(map[string]any)["resources"].(map[string]any)
		limits := resources["limits"].(map[string]any)
		if resources["cpu_idle"] != sent.Template.Containers[0].Resources.CpuIdle {
			t.Errorf("%s: lists cpu_idle %v, serviceOf sends %v", compute, resources["cpu_idle"], sent.Template.Containers[0].Resources.CpuIdle)
		}
		if limits["cpu"] != sent.Template.Containers[0].Resources.Limits["cpu"] || limits["memory"] != sent.Template.Containers[0].Resources.Limits["memory"] {
			t.Errorf("%s: lists limits %v, serviceOf sends %v", compute, limits, sent.Template.Containers[0].Resources.Limits)
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
		t.Errorf("lists ingress %v, the bootstrap sends %v", properties["ingress"], sent.Ingress)
	}
	listed := properties["template"].(map[string]any)
	scaling := listed["scaling"].(map[string]any)
	if int64(scaling["min_instance_count"].(int)) != sent.Template.Scaling.MinInstanceCount ||
		int64(scaling["max_instance_count"].(int)) != sent.Template.Scaling.MaxInstanceCount {
		t.Errorf("lists scaling %v, the bootstrap sends %+v", scaling, sent.Template.Scaling)
	}
	resources := listed["containers"].([]any)[0].(map[string]any)["resources"].(map[string]any)
	limits := resources["limits"].(map[string]any)
	if resources["cpu_idle"] != sent.Template.Containers[0].Resources.CpuIdle {
		t.Errorf("lists cpu_idle %v, the bootstrap sends %v", resources["cpu_idle"], sent.Template.Containers[0].Resources.CpuIdle)
	}
	if limits["cpu"] != sent.Template.Containers[0].Resources.Limits["cpu"] || limits["memory"] != sent.Template.Containers[0].Resources.Limits["memory"] {
		t.Errorf("lists limits %v, the bootstrap sends %v", limits, sent.Template.Containers[0].Resources.Limits)
	}
}

func TestEveryBootstrapItemHasAInventory(t *testing.T) {
	t.Parallel()

	names := names.New("ocel", "acme-prod")
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		for _, item := range bootstrap.Resources(names, tier, false) {
			if _, listed := item.Inventory("europe-west1"); !listed {
				t.Errorf("%s provisions a %s the inventory has no name for", tier, item.ID())
			}
		}
	}
}

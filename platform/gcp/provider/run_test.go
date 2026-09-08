package gcp

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestAServerlessRevisionScalesToNothingAndIsBilledPerRequest(t *testing.T) {
	desired := serviceOf(serving{
		service: "ocel-shop-prod-web",
		image:   "europe-west1-docker.pkg.dev/acme/ocel/web@sha256:abc",
		account: "ocel-production@acme.iam.gserviceaccount.com",
		compute: providerkit.ComputeServerless,
	})

	template := desired.Template
	if template.Scaling.MinInstanceCount != 0 {
		t.Errorf("a function keeps %d instances warm, want none: it is billed per request", template.Scaling.MinInstanceCount)
	}
	container := template.Containers[0]
	if !container.Resources.CpuIdle {
		t.Error("a function holds its cpu between requests, want it idle: that is what request-based billing is")
	}
	if container.StartupProbe != nil {
		t.Error("a function carries a startup probe, and nothing declared a path to probe")
	}
	if got := container.Resources.Limits; got["cpu"] != "1" || got["memory"] != "512Mi" {
		t.Errorf("a revision asks for %v, want one vCPU and 512Mi", got)
	}
	if len(container.Ports) != 1 || container.Ports[0].ContainerPort != providerkit.InjectedPort {
		t.Errorf("a revision listens on %v, want %d, which is the port every image ocel builds binds",
			container.Ports, providerkit.InjectedPort)
	}
	if template.ServiceAccount != "ocel-production@acme.iam.gserviceaccount.com" {
		t.Errorf("a revision runs as %q, want the class's own runtime account", template.ServiceAccount)
	}
}

func TestAContainerRevisionKeepsAnInstanceUpAndIsProbedOnItsOwnPath(t *testing.T) {
	desired := serviceOf(serving{
		service: "ocel-shop-prod-api",
		image:   "europe-west1-docker.pkg.dev/acme/ocel/api@sha256:abc",
		compute: providerkit.ComputeContainer,
		health:  "/healthz",
	})

	template := desired.Template
	if template.Scaling.MinInstanceCount != 1 {
		t.Errorf("a container keeps %d instances up, want one: it is billed for the instance rather than the request", template.Scaling.MinInstanceCount)
	}
	container := template.Containers[0]
	if container.Resources.CpuIdle {
		t.Error("a container's cpu idles between requests, and a container is paid for by the instance: it keeps its cpu")
	}
	if container.StartupProbe == nil || container.StartupProbe.HttpGet == nil {
		t.Fatal("a container carries no startup probe, and up means a 2xx on the path the wire named")
	}
	if got := container.StartupProbe.HttpGet; got.Path != "/healthz" || got.Port != providerkit.InjectedPort {
		t.Errorf("a container is probed at %v, want /healthz on %d", got, providerkit.InjectedPort)
	}
}

func TestARevisionCarriesEveryValueTheDeployResolvedInOneOrder(t *testing.T) {
	desired := serviceOf(serving{
		service: "ocel-shop-prod-web",
		image:   "web@sha256:abc",
		compute: providerkit.ComputeServerless,
		env:     map[string]string{"DATABASE_URL": "postgres://", "API_KEY": "k"},
	})

	var carried []string
	for _, entry := range desired.Template.Containers[0].Env {
		carried = append(carried, entry.Name+"="+entry.Value)
	}
	if got := strings.Join(carried, " "); got != "API_KEY=k DATABASE_URL=postgres://" {
		t.Errorf("a revision carries %q, want every value in one order, so an unchanged deploy asks for no new revision", got)
	}
}

func TestTrafficIsPinnedToOneRevisionByName(t *testing.T) {
	pinned := trafficTo("ocel-shop-prod-web-00007-abc")

	if len(pinned) != 1 {
		t.Fatalf("traffic is split %d ways, want all of it on the revision this release stood up", len(pinned))
	}
	if pinned[0].Type != trafficByRevision {
		t.Errorf("traffic is allocated by %q, want %q: the latest revision is whatever ran last, which is not what this release promised",
			pinned[0].Type, trafficByRevision)
	}
	if pinned[0].Revision != "ocel-shop-prod-web-00007-abc" || pinned[0].Percent != 100 {
		t.Errorf("traffic = %+v, want all of it on the named revision", pinned[0])
	}
}

func TestThePublicIsBoundToInvokeAServiceOnceAndNotTwice(t *testing.T) {
	held := []*policyBinding{{Role: invokerRole, Members: []string{"user:someone@acme.com"}}}

	bound, changed := invokable(held)
	if !changed {
		t.Fatal("a policy with nobody public bound was left alone, and a service nobody may invoke answers nothing")
	}
	if _, again := invokable(bound); again {
		t.Error("binding the public a second time changed the policy again, so every deploy would write one")
	}
}

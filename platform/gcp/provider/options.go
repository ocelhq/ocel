package gcp

import (
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

type Options struct {
	Project     string                `json:"project,omitempty" doc:"The Google Cloud project to deploy into. Left off, from GOOGLE_CLOUD_PROJECT, CLOUDSDK_CORE_PROJECT or gcloud config."`
	OwnDatabase bool                  `json:"ownDatabase,omitempty" doc:"Keep this project's values, runs and tag records in a Firestore database of its own, which no other project's apps can reach. Left off, they are kept in the one database every project of the tier shares. Fixed by the project's first deploy or value."`
	Region      string                `json:"region" examples:"africa-south1,asia-east1,asia-east2,asia-northeast1,asia-northeast2,asia-northeast3,asia-south1,asia-south2,asia-southeast1,asia-southeast2,asia-southeast3,australia-southeast1,australia-southeast2,europe-central2,europe-north1,europe-north2,europe-southwest1,europe-west1,europe-west10,europe-west12,europe-west2,europe-west3,europe-west4,europe-west6,europe-west8,europe-west9,me-central1,me-central2,me-west1,northamerica-northeast1,northamerica-northeast2,northamerica-south1,southamerica-east1,southamerica-west1,us-central1,us-east1,us-east4,us-east5,us-south1,us-west1,us-west2,us-west3,us-west4" doc:"The region to deploy into, such as us-central1, never a zone such as us-central1-f."`
	Apps        map[string]AppOptions `json:"apps,omitempty" doc:"Cloud Run settings for each app, keyed by the app's name."`
	Network     *NetworkOptions       `json:"network,omitempty" doc:"A VPC network of the project, and its subnetwork in the region, that this project's apps, kv stores and databases join instead of the tier's own network. Its owner makes the service connection policies Cloud SQL and Memorystore connect through, and ocel changes nothing on it."`
}

type NetworkOptions struct {
	Network    string `json:"network" doc:"The name of the VPC network, which must be in the project deployed into."`
	Subnetwork string `json:"subnetwork" doc:"The name of the network's subnetwork in the region: the apps' Direct VPC egress leaves from it."`
}

func (NetworkOptions) Doc() string {
	return "A VPC network of the project and its subnetwork in the region."
}

type AppOptions struct {
	Network bool        `json:"network,omitempty" doc:"Send the app's traffic to private addresses over the network named by option network, as an app bound to a postgres or kv is. Left off, only an app bound to one does."`
	Billing run.Billing `json:"billing,omitempty" enum:"request,instance" doc:"How Cloud Run bills the app's services. request bills by the request and takes the CPU away once a response ends, so work after it, such as after(), waitUntil, a background page refresh or a tag purge, may stall until the next request or never run. instance bills the instance for its whole life, up to 15 minutes idle after the last request, and an idle instances.min instance in full. Left off, instance for a Next app or a container, and request for every other app."`
}

func (AppOptions) Doc() string { return "The Cloud Run settings of one app." }

func (o Options) refuseUnknownBilling() error {
	for _, app := range slices.Sorted(maps.Keys(o.Apps)) {
		switch o.Apps[app].Billing {
		case "", run.BillingRequest, run.BillingInstance:
			continue
		}
		return refusal.Refuse(refusal.CodeInvalid, "option %q is %q, and Cloud Run bills by %s or %s: name one",
			"apps."+app+".billing", o.Apps[app].Billing, run.BillingRequest, run.BillingInstance)
	}
	return nil
}

func (o Options) refuseUnusableNetwork() error {
	if o.Network == nil {
		for _, app := range slices.Sorted(maps.Keys(o.Apps)) {
			if o.Apps[app].Network {
				return refusal.Refuse(refusal.CodeInvalid, "option %q reaches the network option %q names, and it names none: name one, or remove it", "apps."+app+".network", "network")
			}
		}
		return nil
	}
	for _, field := range []struct{ option, name string }{{"network.network", o.Network.Network}, {"network.subnetwork", o.Network.Subnetwork}} {
		switch {
		case strings.TrimSpace(field.name) == "":
			return refusal.Refuse(refusal.CodeInvalid, "option %q names nothing, and the apps join a network by its subnetwork in the region: name it", field.option)
		case strings.Contains(field.name, "/"):
			return refusal.Refuse(refusal.CodeInvalid, "option %q is %q, a path, and a project joins a network only of the project it deploys into: name it by its name alone", field.option, field.name)
		}
	}
	return nil
}

func (o Options) reachesNetwork(app string) bool { return o.Apps[app].Network }

func (o Options) names(namespace provider.Namespace, project string) names.Names {
	named := names.New(namespace, project)
	if o.Network == nil {
		return named
	}
	return named.WithNetwork(o.Network.Network, o.Network.Subnetwork)
}

func (o Options) refuseUnknownApps(apps []provider.AppEntry) error {
	known := make([]string, 0, len(apps))
	for _, app := range apps {
		known = append(known, app.App)
	}
	for _, app := range slices.Sorted(maps.Keys(o.Apps)) {
		if slices.Contains(known, app) {
			continue
		}
		if len(known) == 0 {
			return refusal.Refuse(refusal.CodeInvalid, "option %q names an app, and this project has none: remove it", "apps."+app)
		}
		return refusal.Refuse(refusal.CodeInvalid, "option %q names no app of this project: name one of %s", "apps."+app, strings.Join(known, ", "))
	}
	return nil
}

func (o Options) resolveBilling(app string, compute provider.Compute, servesNext bool) run.Billing {
	chosen := o.Apps[app].Billing
	if chosen == "" && servesNext {
		return run.BillingInstance
	}
	return run.ResolveBilling(chosen, compute)
}

package gcp

import "github.com/ocelhq/ocel/pkg/pricing/ingest"

const (
	serviceArtifactRegistry = "149C-F9EC-3994"
	serviceCloudRun         = "152E-C115-5142"
	serviceScheduler        = "1F14-4801-0E16"
	serviceStorage          = "95FF-2EF5-5EA1"
	serviceSQL              = "9662-B51E-5089"
	servicePubSub           = "A1E8-BE35-7EBC"
	serviceMemorystore      = "A2B5-E0F1-B0F3"
	serviceNetworking       = "E505-1604-58F8"
	serviceFirestore        = "EE2C-7FAC-5E08"
	serviceKMS              = "EE2F-D110-890C"
	serviceSecretManager    = "EE82-7A5E-871C"
	serviceTasks            = "F3A6-D7B7-9BDA"
)

func service(id string) ingest.Service {
	return ingest.Service{Service: id, Where: []ingest.Filter{{Attribute: "usageType", In: []string{"OnDemand"}}}}
}

var ingestion = ingest.Vendor{
	Vendor: vendor,
	Source: ingest.SourceGCPCatalog,
	Services: []ingest.Service{
		service(serviceArtifactRegistry),
		service(serviceCloudRun),
		service(serviceScheduler),
		service(serviceStorage),
		service(serviceSQL),
		service(servicePubSub),
		service(serviceMemorystore),
		service(serviceNetworking),
		service(serviceFirestore),
		service(serviceKMS),
		service(serviceSecretManager),
		service(serviceTasks),
	},
}

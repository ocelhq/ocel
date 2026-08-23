package deploy

import (
	"github.com/ocelhq/ocel/pkg/naming"
	kitpulumi "github.com/ocelhq/ocel/pkg/providerkit/pulumi"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type PulumiAccess struct {
	Region        string
	BackendURL    string
	Passphrase    string
	PulumiProject string
}

type ObjectStores struct {
	Uploader           ArtifactUploader
	ArtifactBucket     string
	AssetBucket        string
	CacheStoreBucket   string
	CacheStoreUploader ArtifactUploader
}

type Reporting struct {
	Tracer      Tracer
	StageReport func(StageID) func(string)
}

func (r Reporting) stage(s Stage) func(string) {
	if r.StageReport == nil {
		return func(string) {}
	}
	return nilSafe(r.StageReport(s.ID))
}

type ISRWriterAccess struct {
	Endpoint      string
	BootstrapCred string
	Seed          string
}

type Teardown struct {
	Pulumi   PulumiAccess
	engine   kitpulumi.Engine
	Slug     string
	Env      string
	Stacks   StackIndex
	Stores   ObjectStores
	Report   Reporting
	Realized *Realized
}

func (t Teardown) project() string {
	return naming.Sanitize(t.Slug)
}

func (t Teardown) forStack(name naming.StackName) StackTeardown {
	return StackTeardown{
		Pulumi:   t.Pulumi,
		engine:   t.engine,
		Project:  t.project(),
		Stack:    name,
		Stacks:   t.Stacks,
		Realized: t.Realized,
	}
}

type Reclamation struct {
	Teardown
	ISRWriter ISRWriterAccess
}

type ProjectTeardown struct {
	Teardown
	Values ValueStore
	DNS    edge.DNSWriter
}

func (cfg Config) objectStores() ObjectStores {
	return ObjectStores{
		Uploader:           cfg.Uploader,
		ArtifactBucket:     cfg.ArtifactBucket,
		AssetBucket:        cfg.AssetBucket,
		CacheStoreBucket:   cfg.CacheStoreBucket,
		CacheStoreUploader: cfg.CacheStoreUploader,
	}
}

func (cfg Config) reporting() Reporting {
	return Reporting{Tracer: cfg.Tracer, StageReport: cfg.StageReport}
}

func (cfg Config) isrWriter() ISRWriterAccess {
	return ISRWriterAccess{
		Endpoint:      cfg.ISRWriterEndpoint,
		BootstrapCred: cfg.ISRWriterBootstrapCred,
		Seed:          cfg.ISRWriterSeed,
	}
}

func (cfg Config) workerFacts() WorkerFacts {
	return WorkerFacts{
		Region:             cfg.Region,
		StateTable:         cfg.StateTable,
		AssetBucket:        cfg.AssetBucket,
		ImageOptimizerURL:  cfg.ImageOptimizerURL,
		RevalidateQueueURL: cfg.RevalidateQueueURL,
		EdgeAccessKeyID:    cfg.EdgeAccessKeyID,
		EdgeSecretKey:      cfg.EdgeSecretKey,
	}
}

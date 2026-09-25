package deploy

import "github.com/ocelhq/ocel/platform/aws/provider/payloads"

type ObjectStores struct {
	Objects           payloads.ObjectStore
	ArtifactBucket    string
	AssetBucket       string
	CacheStoreBucket  string
	CacheStoreObjects payloads.ObjectStore
}

type ISRWriterAccess struct {
	Endpoint      string
	BootstrapCred string
	Seed          string
}

func (cfg Config) objectStores() ObjectStores {
	return ObjectStores{
		Objects:           cfg.Objects,
		ArtifactBucket:    cfg.ArtifactBucket,
		AssetBucket:       cfg.AssetBucket,
		CacheStoreBucket:  cfg.CacheStoreBucket,
		CacheStoreObjects: cfg.CacheStoreObjects,
	}
}

func (cfg Config) isrWriter() ISRWriterAccess {
	return ISRWriterAccess{
		Endpoint:      cfg.ISRWriterEndpoint,
		BootstrapCred: cfg.ISRWriterBootstrapCred,
		Seed:          cfg.ISRWriterSeed,
	}
}

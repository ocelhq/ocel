package deploy

import (
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

type ObjectStores struct {
	Objects           payloads.ObjectStore
	ArtifactBucket    string
	AssetBucket       string
	CacheStoreBucket  string
	CacheStoreObjects payloads.ObjectStore
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

func (cfg Config) isrWriter() cloudflare.ISRWriter {
	return cloudflare.ISRWriter{
		Endpoint:            cfg.ISRWriterEndpoint,
		BootstrapCredential: cfg.ISRWriterBootstrapCredential,
		Seed:                cfg.ISRWriterSeed,
	}
}

package deploy

type ObjectStores struct {
	Uploader           ArtifactUploader
	ArtifactBucket     string
	AssetBucket        string
	CacheStoreBucket   string
	CacheStoreUploader ArtifactUploader
}

type ISRWriterAccess struct {
	Endpoint      string
	BootstrapCred string
	Seed          string
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

func (cfg Config) isrWriter() ISRWriterAccess {
	return ISRWriterAccess{
		Endpoint:      cfg.ISRWriterEndpoint,
		BootstrapCred: cfg.ISRWriterBootstrapCred,
		Seed:          cfg.ISRWriterSeed,
	}
}

package resources

import (
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/bucket"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/kv"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/postgres"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/realtime"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/task"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/topic"
)

type Clients struct {
	Artifacts   component.Artifacts
	Clusters    postgres.ClustersAPI
	KVTokens    kv.Tokens
	SigningKeys realtime.Keys
}

func Registry(c Clients) component.Registry {
	return component.NewRegistry(
		postgres.New(c.Clusters),
		bucket.New(c.Artifacts),
		kv.New(c.KVTokens),
		task.New(),
		topic.New(),
		realtime.New(c.Artifacts, c.SigningKeys),
	)
}

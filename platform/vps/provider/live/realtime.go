package live

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
)

const RootRealtime records.Root = "realtime"

const RealtimeSocketPath = "/.well-known/ocel-realtime"

type RealtimeNamespace struct {
	VerifyKey string `json:"verifyKey"`
}

func RealtimePartition(tier environment.Tier) records.Partition {
	return records.Partition{Tier: tier, Root: RootRealtime}
}

func RealtimeNamespaceKey(tier environment.Tier, project, env, namespace string) records.Key {
	return RealtimePartition(tier).Key(project, env, namespace)
}

func RealtimeNamespacesUnder(tier environment.Tier, project, env string) (records.Partition, []string) {
	return RealtimePartition(tier), []string{project, env}
}

package live

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
)

const RealtimeSocketPath = "/.well-known/ocel-realtime"

type RealtimeNamespace struct {
	VerifyKey string `json:"verifyKey"`
}

func RealtimePartition(tier environment.Tier) keyvalue.Partition {
	return keyvalue.Partition{Tier: tier, Root: keyvalue.RootRealtime}
}

func RealtimeNamespaceKey(tier environment.Tier, project, env, namespace string) keyvalue.Key {
	return RealtimePartition(tier).Key(project, env, namespace)
}

func RealtimeNamespacesUnder(tier environment.Tier, project, env string) (keyvalue.Partition, []string) {
	return RealtimePartition(tier), []string{project, env}
}

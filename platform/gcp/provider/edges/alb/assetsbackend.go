package alb

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
)

const agentAppearsAttempts = 20

func (e *Edge) raiseFront(ctx context.Context, spec albresources.LoadBalancerSpec, progress progress.Log) (map[string]string, error) {
	for attempt := 1; ; attempt++ {
		outputs, err := e.deps.Engine.Stacks.UpLoadBalancer(ctx, spec, progress)
		if err == nil {
			return outputs, nil
		}
		if !isUnseenAgent(err, spec.Assets) || attempt == agentAppearsAttempts {
			return outputs, err
		}
		if !e.deps.WaitBeforeRetry(ctx, attempt) {
			return nil, err
		}
	}
}

func isUnseenAgent(err error, assets *albresources.AssetsBucket) bool {
	if assets == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, strings.TrimPrefix(assets.Agent, "serviceAccount:")) && strings.Contains(message, "does not exist")
}

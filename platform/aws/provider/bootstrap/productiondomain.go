package bootstrap

import (
	"encoding/json"
	"fmt"
	"maps"

	"github.com/ocelhq/ocel/platform/aws/provider/domains"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func ReadProduction(state edge.StackState) (domains.Settlement, error) {
	raw := state[edge.StackKeyProductionDomains]
	if raw == "" {
		return domains.Settlement{}, nil
	}
	var recorded domains.Settlement
	if err := json.Unmarshal([]byte(raw), &recorded); err != nil {
		return domains.Settlement{}, fmt.Errorf("parse the production domains recorded on this stack: %w", err)
	}
	return recorded, nil
}

func WithProduction(state edge.StackState, recorded domains.Settlement) (edge.StackState, error) {
	next := maps.Clone(state)
	if next == nil {
		next = edge.StackState{}
	}
	if recorded.Empty() {
		delete(next, edge.StackKeyProductionDomains)
		return next, nil
	}
	payload, err := json.Marshal(recorded)
	if err != nil {
		return nil, fmt.Errorf("record the production domains of this stack: %w", err)
	}
	next[edge.StackKeyProductionDomains] = string(payload)
	return next, nil
}

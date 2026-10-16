package stackrecords

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
)

type partsUsed struct {
	Parts []string `json:"parts,omitempty"`
}

func RecordPartsUsed(ctx context.Context, store keyvalue.Store, tier environment.Tier, project string, parts []string) error {
	return keyvalue.Change(ctx, store, PartsUsedKey(tier, project), func(recorded keyvalue.Entry) ([]byte, bool, error) {
		var prior partsUsed
		if len(recorded.Value) > 0 {
			if err := json.Unmarshal(recorded.Value, &prior); err != nil {
				return nil, false, fmt.Errorf("read the parts %s uses: %w", project, err)
			}
		}
		if slices.Equal(prior.Parts, parts) && len(recorded.Value) > 0 {
			return nil, false, nil
		}
		value, err := json.Marshal(partsUsed{Parts: parts})
		return value, true, err
	})
}

func ListPartUsers(ctx context.Context, store keyvalue.Store, tier environment.Tier) (map[string][]string, error) {
	entries, err := store.List(ctx, ProjectsPartition(tier))
	if err != nil {
		return nil, fmt.Errorf("read the projects of the %s tier: %w", tier, err)
	}
	live := map[string]bool{}
	used := map[string][]string{}
	for _, entry := range entries {
		switch path := entry.Key.Path; {
		case len(path) == 1:
			live[path[0]] = true
		case len(path) == 2 && path[1] == partsSegment && len(entry.Value) > 0:
			var recorded partsUsed
			if err := json.Unmarshal(entry.Value, &recorded); err != nil {
				return nil, fmt.Errorf("read the parts %s uses: %w", path[0], err)
			}
			for _, part := range recorded.Parts {
				used[part] = append(used[part], path[0])
			}
		}
	}
	users := map[string][]string{}
	for part, projects := range used {
		for _, project := range projects {
			if live[project] {
				users[part] = append(users[part], project)
			}
		}
		slices.Sort(users[part])
	}
	return users, nil
}

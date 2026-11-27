package variablestore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
)

func (s Store) PurgeEnvironment(ctx context.Context, scope Scope, preview string) error {
	if scope.Tier != environment.TierPreview || preview == "" || preview == TierWideEnvironment {
		return fmt.Errorf("purge %s's preview values: name one preview environment in the preview tier", scope.Project)
	}
	entries, err := s.KeyValues.List(ctx, ValuesPartition(scope))
	if err != nil {
		return fmt.Errorf("read %s's values: %w", scope.Project, err)
	}
	for _, entry := range entries {
		path := entry.Key.Path
		cell := len(path) == 4 && path[0] == cellsSegment
		history := len(path) == 5 && path[0] == historySegment
		if (!cell && !history) || path[3] != preview {
			continue
		}
		err := records.ForgetMatching(ctx, s.KeyValues, entry.Key, func(current records.Entry) (bool, error) {
			if cell {
				var stored storedValue
				if err := json.Unmarshal(current.Value, &stored); err != nil {
					return false, err
				}
				if stored.Target != nil {
					at, _ := cellOf(current.Key)
					if err := s.unindexReference(ctx, scope, at, stored.Target); err != nil {
						return false, err
					}
				}
			}
			return true, nil
		})
		if err != nil {
			return fmt.Errorf("purge %s: %w", entry.Key, err)
		}
	}
	return nil
}

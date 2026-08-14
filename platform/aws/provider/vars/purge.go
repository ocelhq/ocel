package vars

import (
	"context"
	"fmt"

	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const purgeBatch = 100

func (s *Store) Purge(ctx context.Context, slug string) (int, error) {
	if slug == "" {
		return 0, fmt.Errorf("a project slug is required")
	}

	if err := s.purgeLinks(ctx, slug); err != nil {
		return 0, err
	}

	pk := PartitionKey(slug, s.Class)
	items, err := s.query(ctx, pk, "", true)
	if err != nil {
		return 0, err
	}

	keys := make([]map[string]ddbtypes.AttributeValue, 0, len(items))
	for _, stored := range items {
		keys = append(keys, pointKey(pk, stored.SK))
	}
	if err := s.deleteAll(ctx, keys, slug); err != nil {
		return 0, err
	}
	return len(keys), nil
}

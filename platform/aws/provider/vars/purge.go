package vars

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const purgeBatch = 100

func (s *Store) Purge(ctx context.Context, slug string) (int, error) {
	if slug == "" {
		return 0, fmt.Errorf("a project slug is required")
	}

	pk := PartitionKey(slug, s.Class)
	items, err := s.query(ctx, pk, "", true)
	if err != nil {
		return 0, err
	}

	removed := 0
	for start := 0; start < len(items); start += purgeBatch {
		batch := items[start:min(start+purgeBatch, len(items))]
		writes := make([]ddbtypes.TransactWriteItem, 0, len(batch))
		for _, stored := range batch {
			writes = append(writes, ddbtypes.TransactWriteItem{Delete: &ddbtypes.Delete{
				TableName: aws.String(s.Table),
				Key:       pointKey(pk, stored.SK),
			}})
		}
		if _, err := s.Dynamo.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: writes}); err != nil {
			return removed, fmt.Errorf("remove %s's stored values: %w", slug, err)
		}
		removed += len(batch)
	}
	return removed, nil
}

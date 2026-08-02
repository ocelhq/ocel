package vars

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// purgeBatch is how many rows one removal transaction carries. DynamoDB caps a
// transaction at 100 items, and a delete carries only its key, so that cap is
// the only thing bounding it.
const purgeBatch = 100

// Purge removes everything a project holds in this class: its current values,
// its tombstones, and its version history. It is the teardown's counterpart to
// Delete, and it is a removal rather than a tombstone because there is no next
// write whose version sequence a tombstone would be keeping a place for — the
// project itself is going.
//
// History goes with the values. A version row carries the ciphertext its value
// once had, so leaving history behind would leave the store holding secrets of
// a project the operator was told was gone, which is the one outcome a teardown
// exists to prevent.
//
// It is idempotent: a partition an earlier run already emptied removes nothing
// and succeeds, so a best-effort teardown resumes on a re-run.
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

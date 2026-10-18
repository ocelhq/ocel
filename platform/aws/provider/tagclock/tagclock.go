package tagclock

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ocelhq/ocel/pkg/naming"
)

const (
	sortAttribute = "sk"

	IndexName = "gsi1"

	tagNamespaceAttribute = "gsi1pk"
	tagSortKey            = "#META"
)

const (
	batchSize = 25

	unprocessedAttempts = 8
	unprocessedBase     = 50 * time.Millisecond
	unprocessedCeiling  = 2 * time.Second
)

type DynamoAPI interface {
	BatchWriteItem(context.Context, *dynamodb.BatchWriteItemInput, ...func(*dynamodb.Options)) (*dynamodb.BatchWriteItemOutput, error)
	Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
}

type Table struct {
	Dynamo DynamoAPI
	Table  string

	wait func(context.Context, time.Duration) error
}

func (s *Table) Sweep(ctx context.Context, project string, stack naming.StackName) error {
	partitions, err := s.tagPartitions(ctx, naming.ISRTagPrefix(project, stack))
	if err != nil {
		return fmt.Errorf("drop the tag clock of stack %s/%s: %w", project, stack, err)
	}
	for batch := range slices.Chunk(partitions, batchSize) {
		requests := make([]ddbtypes.WriteRequest, 0, len(batch))
		for _, pk := range batch {
			requests = append(requests, ddbtypes.WriteRequest{DeleteRequest: &ddbtypes.DeleteRequest{Key: item(pk, tagSortKey)}})
		}
		if err := s.deleteAll(ctx, requests); err != nil {
			return fmt.Errorf("drop the tag clock of stack %s/%s: %w", project, stack, err)
		}
	}
	return nil
}

func (s *Table) tagPartitions(ctx context.Context, namespace string) ([]string, error) {
	var (
		out   []string
		start map[string]ddbtypes.AttributeValue
	)
	for {
		page, err := s.Dynamo.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(s.Table),
			IndexName:              aws.String(IndexName),
			KeyConditionExpression: aws.String("#ns = :ns"),
			ProjectionExpression:   aws.String("#pk, #sk"),
			ExpressionAttributeNames: map[string]string{
				"#ns": tagNamespaceAttribute,
				"#pk": "pk",
				"#sk": sortAttribute,
			},
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
				":ns": &ddbtypes.AttributeValueMemberS{Value: namespace},
			},
			ExclusiveStartKey: start,
		})
		if err != nil {
			return nil, err
		}
		for _, entry := range page.Items {
			pk, ok := entry["pk"].(*ddbtypes.AttributeValueMemberS)
			sk, sorted := entry[sortAttribute].(*ddbtypes.AttributeValueMemberS)
			if !ok || pk.Value == "" || !sorted || sk.Value != tagSortKey {
				continue
			}
			out = append(out, pk.Value)
		}
		if len(page.LastEvaluatedKey) == 0 {
			return out, nil
		}
		start = page.LastEvaluatedKey
	}
}

func (s *Table) deleteAll(ctx context.Context, requests []ddbtypes.WriteRequest) error {
	for attempt := range unprocessedAttempts {
		if attempt > 0 {
			if err := s.backoff(ctx, attempt-1); err != nil {
				return err
			}
		}
		out, err := s.Dynamo.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{
			RequestItems: map[string][]ddbtypes.WriteRequest{s.Table: requests},
		})
		if err != nil {
			return err
		}
		requests = out.UnprocessedItems[s.Table]
		if len(requests) == 0 {
			return nil
		}
	}
	return fmt.Errorf("DynamoDB left %d tag rows unprocessed after %d attempts", len(requests), unprocessedAttempts)
}

func (s *Table) backoff(ctx context.Context, attempt int) error {
	ceiling := min(unprocessedBase<<attempt, unprocessedCeiling)
	delay := ceiling/2 + rand.N(ceiling/2+1)
	if s.wait != nil {
		return s.wait(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func item(pk, sk string) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{
		"pk":          &ddbtypes.AttributeValueMemberS{Value: pk},
		sortAttribute: &ddbtypes.AttributeValueMemberS{Value: sk},
	}
}

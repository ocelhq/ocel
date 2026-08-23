package tagclock

import (
	"context"
	"fmt"

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

type DynamoAPI interface {
	DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
	Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
}

type Sweeper struct {
	Dynamo DynamoAPI
	Table  string
}

func (s *Sweeper) SweepTagClock(ctx context.Context, project string, stack naming.StackName) error {
	partitions, err := s.tagPartitions(ctx, naming.ISRTagPrefix(project, stack))
	if err != nil {
		return fmt.Errorf("drop the tag clock of stack %s/%s: %w", project, stack, err)
	}
	for _, pk := range partitions {
		if err := s.delete(ctx, item(pk, tagSortKey)); err != nil {
			return fmt.Errorf("drop the tag clock of stack %s/%s: %w", project, stack, err)
		}
	}
	return nil
}

func (s *Sweeper) tagPartitions(ctx context.Context, namespace string) ([]string, error) {
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

func (s *Sweeper) delete(ctx context.Context, entry map[string]ddbtypes.AttributeValue) error {
	_, err := s.Dynamo.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.Table),
		Key:       entry,
	})
	return err
}

func item(pk, sk string) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{
		"pk":          &ddbtypes.AttributeValueMemberS{Value: pk},
		sortAttribute: &ddbtypes.AttributeValueMemberS{Value: sk},
	}
}

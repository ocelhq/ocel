package stackindex

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const (
	projectsPartition = "OCELSTACKS"
	scopePartition    = "OCELSTACKS#"
	scopeSeparator    = "--"
)

type DynamoAPI interface {
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
	Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
}

type Index struct {
	Dynamo DynamoAPI
	Table  string
}

func ScopeOf(stackName string) (string, error) {
	scope, _, ok := strings.Cut(stackName, scopeSeparator)
	if !ok || scope == "" {
		return "", fmt.Errorf("stack name %q carries no project scope", stackName)
	}
	return scope, nil
}

func (ix *Index) AddProject(ctx context.Context, scope string) error {
	if scope == "" {
		return fmt.Errorf("index a project: no scope")
	}
	if err := ix.put(ctx, projectsPartition, scope); err != nil {
		return fmt.Errorf("index project %s: %w", scope, err)
	}
	return nil
}

func (ix *Index) AddStack(ctx context.Context, stackName string) error {
	scope, err := ScopeOf(stackName)
	if err != nil {
		return err
	}
	if err := ix.put(ctx, scopePartition+scope, stackName); err != nil {
		return fmt.Errorf("index stack %s: %w", stackName, err)
	}
	return nil
}

func (ix *Index) RemoveStack(ctx context.Context, stackName string) error {
	scope, err := ScopeOf(stackName)
	if err != nil {
		return err
	}
	if err := ix.delete(ctx, scopePartition+scope, stackName); err != nil {
		return fmt.Errorf("drop stack %s from the index: %w", stackName, err)
	}
	return nil
}

func (ix *Index) RemoveProject(ctx context.Context, scope string) error {
	if scope == "" {
		return fmt.Errorf("drop a project from the index: no scope")
	}
	if err := ix.delete(ctx, projectsPartition, scope); err != nil {
		return fmt.Errorf("drop project %s from the index: %w", scope, err)
	}
	return nil
}

func (ix *Index) Stacks(ctx context.Context, scope string) ([]string, error) {
	if scope == "" {
		return nil, fmt.Errorf("list a project's stacks: no scope")
	}
	names, err := ix.sortKeys(ctx, scopePartition+scope)
	if err != nil {
		return nil, fmt.Errorf("list %s's stacks: %w", scope, err)
	}
	return names, nil
}

func (ix *Index) Projects(ctx context.Context) ([]string, error) {
	scopes, err := ix.sortKeys(ctx, projectsPartition)
	if err != nil {
		return nil, fmt.Errorf("list indexed projects: %w", err)
	}
	return scopes, nil
}

func (ix *Index) put(ctx context.Context, pk, sk string) error {
	_, err := ix.Dynamo.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ix.Table),
		Item:      key(pk, sk),
	})
	return err
}

func (ix *Index) delete(ctx context.Context, pk, sk string) error {
	_, err := ix.Dynamo.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(ix.Table),
		Key:       key(pk, sk),
	})
	return err
}

func (ix *Index) sortKeys(ctx context.Context, pk string) ([]string, error) {
	var (
		out   []string
		start map[string]ddbtypes.AttributeValue
	)
	for {
		page, err := ix.Dynamo.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(ix.Table),
			KeyConditionExpression: aws.String("#pk = :pk"),
			ProjectionExpression:   aws.String("#sk"),
			ExpressionAttributeNames: map[string]string{
				"#pk": "pk",
				"#sk": "sk",
			},
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
				":pk": &ddbtypes.AttributeValueMemberS{Value: pk},
			},
			ExclusiveStartKey: start,
		})
		if err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			sk, ok := item["sk"].(*ddbtypes.AttributeValueMemberS)
			if !ok || sk.Value == "" {
				continue
			}
			out = append(out, sk.Value)
		}
		if len(page.LastEvaluatedKey) == 0 {
			return out, nil
		}
		start = page.LastEvaluatedKey
	}
}

func key(pk, sk string) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{
		"pk": &ddbtypes.AttributeValueMemberS{Value: pk},
		"sk": &ddbtypes.AttributeValueMemberS{Value: sk},
	}
}

package stackindex

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ocelhq/ocel/pkg/naming"
)

const (
	projectsPartition = "PROJECTS"
	sortAttribute     = "sk"
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

func (ix *Index) AddProject(ctx context.Context, project string) error {
	if err := naming.Validate("project", project); err != nil {
		return fmt.Errorf("index a project: %w", err)
	}
	if err := ix.put(ctx, item(projectsPartition, naming.ProjectKey(project))); err != nil {
		return fmt.Errorf("index project %s: %w", project, err)
	}
	return nil
}

func (ix *Index) RemoveProject(ctx context.Context, project string) error {
	if err := naming.Validate("project", project); err != nil {
		return fmt.Errorf("drop a project from the index: %w", err)
	}
	if err := ix.delete(ctx, item(projectsPartition, naming.ProjectKey(project))); err != nil {
		return fmt.Errorf("drop project %s from the index: %w", project, err)
	}
	return nil
}

func (ix *Index) AddStack(ctx context.Context, project string, stack naming.StackName) error {
	if err := readable(project, stack); err != nil {
		return fmt.Errorf("index a stack: %w", err)
	}
	if err := ix.put(ctx, item(naming.ProjectKey(project), naming.StackKey(project, stack))); err != nil {
		return fmt.Errorf("index stack %s/%s: %w", project, stack, err)
	}
	return nil
}

func (ix *Index) RemoveStack(ctx context.Context, project string, stack naming.StackName) error {
	if err := readable(project, stack); err != nil {
		return fmt.Errorf("drop a stack from the index: %w", err)
	}
	if err := ix.delete(ctx, item(naming.ProjectKey(project), naming.StackKey(project, stack))); err != nil {
		return fmt.Errorf("drop stack %s/%s from the index: %w", project, stack, err)
	}
	return nil
}

func (ix *Index) Stacks(ctx context.Context, project string) ([]naming.StackName, error) {
	if err := naming.Validate("project", project); err != nil {
		return nil, fmt.Errorf("list a project's stacks: %w", err)
	}
	keys, err := ix.values(ctx, naming.ProjectKey(project), sortAttribute)
	if err != nil {
		return nil, fmt.Errorf("list %s's stacks: %w", project, err)
	}
	var stacks []naming.StackName
	for _, key := range keys {
		_, stack, err := naming.ParseStackKey(key)
		if err != nil {
			return nil, fmt.Errorf("list %s's stacks: %w", project, err)
		}
		stacks = append(stacks, stack)
	}
	return stacks, nil
}

func (ix *Index) Projects(ctx context.Context) ([]string, error) {
	keys, err := ix.values(ctx, projectsPartition, sortAttribute)
	if err != nil {
		return nil, fmt.Errorf("list indexed projects: %w", err)
	}
	var projects []string
	for _, key := range keys {
		project, err := naming.ProjectOf(key)
		if err != nil {
			return nil, fmt.Errorf("list indexed projects: %w", err)
		}
		projects = append(projects, project)
	}
	return projects, nil
}

func readable(project string, stack naming.StackName) error {
	if err := naming.Validate("project", project); err != nil {
		return err
	}
	if _, err := naming.ParseStackName(stack.String()); err != nil {
		return err
	}
	return nil
}

func (ix *Index) put(ctx context.Context, entry map[string]ddbtypes.AttributeValue) error {
	_, err := ix.Dynamo.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ix.Table),
		Item:      entry,
	})
	return err
}

func (ix *Index) delete(ctx context.Context, entry map[string]ddbtypes.AttributeValue) error {
	_, err := ix.Dynamo.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(ix.Table),
		Key:       entry,
	})
	return err
}

func (ix *Index) values(ctx context.Context, pk, attribute string) ([]string, error) {
	var (
		out   []string
		start map[string]ddbtypes.AttributeValue
	)
	for {
		page, err := ix.Dynamo.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(ix.Table),
			ConsistentRead:         aws.Bool(true),
			KeyConditionExpression: aws.String("#pk = :pk"),
			ProjectionExpression:   aws.String("#value"),
			ExpressionAttributeNames: map[string]string{
				"#pk":    "pk",
				"#value": attribute,
			},
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
				":pk": &ddbtypes.AttributeValueMemberS{Value: pk},
			},
			ExclusiveStartKey: start,
		})
		if err != nil {
			return nil, err
		}
		for _, entry := range page.Items {
			value, ok := entry[attribute].(*ddbtypes.AttributeValueMemberS)
			if !ok || value.Value == "" {
				continue
			}
			out = append(out, value.Value)
		}
		if len(page.LastEvaluatedKey) == 0 {
			return out, nil
		}
		start = page.LastEvaluatedKey
	}
}

func item(pk, sk string) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{
		"pk":          &ddbtypes.AttributeValueMemberS{Value: pk},
		sortAttribute: &ddbtypes.AttributeValueMemberS{Value: sk},
	}
}

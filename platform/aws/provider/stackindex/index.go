package stackindex

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ocelhq/ocel/pkg/naming"
)

const (
	projectsPartition = "PROJECTS"
	sortAttribute     = "sk"

	IndexName = "gsi1"

	tagNamespaceAttribute = "gsi1pk"
	tagSortKey            = "#META"

	featuresAttribute = "features"
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

func (ix *Index) AddProject(ctx context.Context, project string, features []string) error {
	if err := naming.Validate("project", project); err != nil {
		return fmt.Errorf("index a project: %w", err)
	}
	entry := item(projectsPartition, naming.ProjectKey(project))
	if len(features) > 0 {
		entry[featuresAttribute] = &ddbtypes.AttributeValueMemberSS{Value: slices.Clone(features)}
	}
	if err := ix.put(ctx, entry); err != nil {
		return fmt.Errorf("index project %s: %w", project, err)
	}
	return nil
}

func (ix *Index) ProjectFeatures(ctx context.Context) (map[string][]string, error) {
	out := map[string][]string{}
	err := ix.eachItem(ctx, projectsPartition, "#sk, #features", map[string]string{
		"#sk":       sortAttribute,
		"#features": featuresAttribute,
	}, func(entry map[string]ddbtypes.AttributeValue) error {
		key, ok := entry[sortAttribute].(*ddbtypes.AttributeValueMemberS)
		if !ok || key.Value == "" {
			return nil
		}
		project, err := naming.ProjectOf(key.Value)
		if err != nil {
			return err
		}
		if features, ok := entry[featuresAttribute].(*ddbtypes.AttributeValueMemberSS); ok {
			out[project] = slices.Clone(features.Value)
		} else {
			out[project] = nil
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read the features indexed projects need: %w", err)
	}
	return out, nil
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
	if err := ix.sweepTagClock(ctx, project, stack); err != nil {
		return fmt.Errorf("drop stack %s/%s from the index: %w", project, stack, err)
	}
	if err := ix.delete(ctx, item(naming.ProjectKey(project), naming.StackKey(project, stack))); err != nil {
		return fmt.Errorf("drop stack %s/%s from the index: %w", project, stack, err)
	}
	return nil
}

func (ix *Index) sweepTagClock(ctx context.Context, project string, stack naming.StackName) error {
	partitions, err := ix.tagPartitions(ctx, naming.ISRTagPrefix(project, stack))
	if err != nil {
		return err
	}
	for _, pk := range partitions {
		if err := ix.delete(ctx, item(pk, tagSortKey)); err != nil {
			return err
		}
	}
	return nil
}

func (ix *Index) tagPartitions(ctx context.Context, namespace string) ([]string, error) {
	var (
		out   []string
		start map[string]ddbtypes.AttributeValue
	)
	for {
		page, err := ix.Dynamo.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(ix.Table),
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

func (ix *Index) eachItem(ctx context.Context, pk, projection string, names map[string]string, visit func(map[string]ddbtypes.AttributeValue) error) error {
	attributes := map[string]string{"#pk": "pk"}
	maps.Copy(attributes, names)
	var start map[string]ddbtypes.AttributeValue
	for {
		page, err := ix.Dynamo.Query(ctx, &dynamodb.QueryInput{
			TableName:                 aws.String(ix.Table),
			ConsistentRead:            aws.Bool(true),
			KeyConditionExpression:    aws.String("#pk = :pk"),
			ProjectionExpression:      aws.String(projection),
			ExpressionAttributeNames:  attributes,
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":pk": &ddbtypes.AttributeValueMemberS{Value: pk}},
			ExclusiveStartKey:         start,
		})
		if err != nil {
			return err
		}
		for _, entry := range page.Items {
			if err := visit(entry); err != nil {
				return err
			}
		}
		if len(page.LastEvaluatedKey) == 0 {
			return nil
		}
		start = page.LastEvaluatedKey
	}
}

func (ix *Index) values(ctx context.Context, pk, attribute string) ([]string, error) {
	var out []string
	err := ix.eachItem(ctx, pk, "#value", map[string]string{"#value": attribute}, func(entry map[string]ddbtypes.AttributeValue) error {
		if value, ok := entry[attribute].(*ddbtypes.AttributeValueMemberS); ok && value.Value != "" {
			out = append(out, value.Value)
		}
		return nil
	})
	return out, err
}

func item(pk, sk string) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{
		"pk":          &ddbtypes.AttributeValueMemberS{Value: pk},
		sortAttribute: &ddbtypes.AttributeValueMemberS{Value: sk},
	}
}

package ports

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	kit "github.com/ocelhq/ocel/pkg/providerkit/ports"
)

const (
	segmentSeparator = "#"

	partitionAttribute = "pk"
	sortAttribute      = "sk"
	bodyAttribute      = "body"
	revisionAttribute  = "rev"
)

var partitionSegments = map[string]int{
	"values":    3,
	"valuerefs": 3,
}

type DynamoAPI interface {
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
	Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
}

type Records struct {
	Dynamo DynamoAPI
	Table  string
}

func Partition(name kit.RecordName) (string, error) {
	pk, _, err := keyOf(name)
	return pk, err
}

func (r Records) Read(ctx context.Context, name kit.RecordName) (kit.Record, error) {
	pk, sk, err := keyOf(name)
	if err != nil {
		return kit.Record{}, err
	}
	out, err := r.Dynamo.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(r.Table),
		Key:            pointKey(pk, sk),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return kit.Record{}, fmt.Errorf("read %s: %w", name, err)
	}
	if len(out.Item) == 0 {
		return kit.Record{}, kit.ErrNoRecord
	}
	return recordOf(name, out.Item), nil
}

func (r Records) Write(ctx context.Context, record kit.Record) (kit.Revision, error) {
	pk, sk, err := keyOf(record.Name)
	if err != nil {
		return "", err
	}
	next, err := mintRevision()
	if err != nil {
		return "", err
	}

	in := &dynamodb.PutItemInput{
		TableName: aws.String(r.Table),
		Item: map[string]ddbtypes.AttributeValue{
			partitionAttribute: &ddbtypes.AttributeValueMemberS{Value: pk},
			sortAttribute:      &ddbtypes.AttributeValueMemberS{Value: sk},
			bodyAttribute:      &ddbtypes.AttributeValueMemberB{Value: record.Bytes},
			revisionAttribute:  &ddbtypes.AttributeValueMemberS{Value: string(next)},
		},
		ExpressionAttributeNames: map[string]string{"#pk": partitionAttribute},
	}
	if record.Revision == "" {
		in.ConditionExpression = aws.String("attribute_not_exists(#pk)")
	} else {
		in.ConditionExpression = aws.String("#rev = :rev")
		in.ExpressionAttributeNames["#rev"] = revisionAttribute
		in.ExpressionAttributeValues = map[string]ddbtypes.AttributeValue{
			":rev": &ddbtypes.AttributeValueMemberS{Value: string(record.Revision)},
		}
	}

	if _, err := r.Dynamo.PutItem(ctx, in); err != nil {
		var failed *ddbtypes.ConditionalCheckFailedException
		if errors.As(err, &failed) {
			return "", kit.ErrStale
		}
		return "", fmt.Errorf("write %s: %w", record.Name, err)
	}
	return next, nil
}

func (r Records) Remove(ctx context.Context, name kit.RecordName, expected kit.Revision) error {
	pk, sk, err := keyOf(name)
	if err != nil {
		return err
	}
	_, err = r.Dynamo.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName:                           aws.String(r.Table),
		Key:                                 pointKey(pk, sk),
		ConditionExpression:                 aws.String("#rev = :rev"),
		ExpressionAttributeNames:            map[string]string{"#rev": revisionAttribute},
		ExpressionAttributeValues:           map[string]ddbtypes.AttributeValue{":rev": &ddbtypes.AttributeValueMemberS{Value: string(expected)}},
		ReturnValuesOnConditionCheckFailure: ddbtypes.ReturnValuesOnConditionCheckFailureAllOld,
	})
	if err == nil {
		return nil
	}
	var failed *ddbtypes.ConditionalCheckFailedException
	if errors.As(err, &failed) {
		if len(failed.Item) == 0 {
			return kit.ErrNoRecord
		}
		return kit.ErrStale
	}
	return fmt.Errorf("remove %s: %w", name, err)
}

func (r Records) List(ctx context.Context, under kit.RecordName) ([]kit.Record, error) {
	pk, sk, err := keyOf(under)
	if err != nil {
		return nil, err
	}

	var out []kit.Record
	var start map[string]ddbtypes.AttributeValue
	for {
		page, err := r.Dynamo.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(r.Table),
			KeyConditionExpression: aws.String("#pk = :pk AND begins_with(#sk, :prefix)"),
			ExpressionAttributeNames: map[string]string{
				"#pk": partitionAttribute,
				"#sk": sortAttribute,
			},
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
				":pk":     &ddbtypes.AttributeValueMemberS{Value: pk},
				":prefix": &ddbtypes.AttributeValueMemberS{Value: sk},
			},
			ConsistentRead:    aws.Bool(true),
			ExclusiveStartKey: start,
		})
		if err != nil {
			return nil, fmt.Errorf("read everything under %s: %w", under, err)
		}
		for _, item := range page.Items {
			name, ok := nameOf(item)
			if !ok {
				continue
			}
			out = append(out, recordOf(name, item))
		}
		if len(page.LastEvaluatedKey) == 0 {
			return out, nil
		}
		start = page.LastEvaluatedKey
	}
}

func keyOf(name kit.RecordName) (string, string, error) {
	if len(name) == 0 {
		return "", "", fmt.Errorf("a record name with no segments names nothing")
	}
	depth := partitionSegments[name[0]]
	if depth == 0 {
		depth = 1
	}
	if len(name) < depth {
		return "", "", fmt.Errorf(
			"%s names %d segments and a %s record partitions on %d: reaching under half a partition would have to walk the whole table",
			name, len(name), name[0], depth)
	}
	return join(name[:depth]), join(name[depth:]) + segmentSeparator, nil
}

func nameOf(item map[string]ddbtypes.AttributeValue) (kit.RecordName, bool) {
	pk, sk := stringAttribute(item, partitionAttribute), stringAttribute(item, sortAttribute)
	if pk == "" || !strings.HasSuffix(sk, segmentSeparator) {
		return nil, false
	}
	name := split(pk)
	if rest := strings.TrimSuffix(sk, segmentSeparator); rest != "" {
		name = append(name, split(rest)...)
	}
	return name, true
}

func recordOf(name kit.RecordName, item map[string]ddbtypes.AttributeValue) kit.Record {
	record := kit.Record{Name: name, Revision: kit.Revision(stringAttribute(item, revisionAttribute))}
	if body, ok := item[bodyAttribute].(*ddbtypes.AttributeValueMemberB); ok {
		record.Bytes = body.Value
	}
	return record
}

func join(segments []string) string {
	escaped := make([]string, 0, len(segments))
	for _, segment := range segments {
		escaped = append(escaped, escape(segment))
	}
	return strings.Join(escaped, segmentSeparator)
}

func split(key string) kit.RecordName {
	segments := strings.Split(key, segmentSeparator)
	out := make(kit.RecordName, 0, len(segments))
	for _, segment := range segments {
		out = append(out, unescape(segment))
	}
	return out
}

func escape(segment string) string {
	return strings.ReplaceAll(strings.ReplaceAll(segment, "%", "%25"), segmentSeparator, "%23")
}

func unescape(segment string) string {
	return strings.ReplaceAll(strings.ReplaceAll(segment, "%23", segmentSeparator), "%25", "%")
}

func pointKey(pk, sk string) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{
		partitionAttribute: &ddbtypes.AttributeValueMemberS{Value: pk},
		sortAttribute:      &ddbtypes.AttributeValueMemberS{Value: sk},
	}
}

func stringAttribute(item map[string]ddbtypes.AttributeValue, name string) string {
	value, ok := item[name].(*ddbtypes.AttributeValueMemberS)
	if !ok {
		return ""
	}
	return value.Value
}

func mintRevision() (kit.Revision, error) {
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return "", fmt.Errorf("mint a revision token: %w", err)
	}
	return kit.Revision(hex.EncodeToString(token)), nil
}

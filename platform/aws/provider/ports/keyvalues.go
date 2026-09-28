package ports

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	segmentSeparator = "#"

	partitionAttribute = "pk"
	sortAttribute      = "sk"
	valueAttribute     = "value"
	revisionAttribute  = "rev"

	conditionalCheckFailed = "ConditionalCheckFailed"
)

type DynamoAPI interface {
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
	Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	TransactWriteItems(context.Context, *dynamodb.TransactWriteItemsInput, ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error)
}

type KeyValues struct {
	Dynamo DynamoAPI
	Tables Tables
}

type Tables interface {
	Table(ctx context.Context, tier environment.Tier) (string, error)
	ValuesTable(ctx context.Context, tier environment.Tier) (string, error)
}

type Table string

func (t Table) Table(context.Context, environment.Tier) (string, error) { return string(t), nil }

func (t Table) ValuesTable(context.Context, environment.Tier) (string, error) { return string(t), nil }

func (s KeyValues) table(ctx context.Context, in keyvalue.Partition) (string, error) {
	if s.Tables == nil {
		return "", nil
	}
	if in.HoldsVariables() {
		return s.Tables.ValuesTable(ctx, in.Tier)
	}
	return s.Tables.Table(ctx, in.Tier)
}

func PartitionKey(in keyvalue.Partition) string {
	return join(in.Segments())
}

func sortKey(path []string) string { return join(path) + segmentSeparator }

func refuseUnbootstrapped(tier environment.Tier) error {
	return refusal.Refuse(refusal.CodeNotReady,
		"this account has no Ocel bootstrap, so there is nowhere to keep an entry.\nRun `%s` to create it, then try again", provider.BootstrapCommand(tier))
}

func isTableMissing(err error) bool {
	var missing *ddbtypes.ResourceNotFoundException
	return errors.As(err, &missing)
}

func (s KeyValues) Read(ctx context.Context, key keyvalue.Key) (keyvalue.Entry, error) {
	if err := keyvalue.RefuseMalformedKey(key); err != nil {
		return keyvalue.Entry{}, err
	}
	table, err := s.table(ctx, key.Partition)
	if err != nil {
		return keyvalue.Entry{}, err
	}
	if table == "" {
		return keyvalue.Entry{}, keyvalue.ErrNotFound
	}
	out, err := s.Dynamo.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(table),
		Key:            pointKey(key),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		if isTableMissing(err) {
			return keyvalue.Entry{}, keyvalue.ErrNotFound
		}
		return keyvalue.Entry{}, fmt.Errorf("read %s: %w", key, err)
	}
	if len(out.Item) == 0 {
		return keyvalue.Entry{}, keyvalue.ErrNotFound
	}
	return entryOf(key, out.Item)
}

func (s KeyValues) Write(ctx context.Context, entry keyvalue.Entry) (keyvalue.Revision, error) {
	written, err := writingOf(entry)
	if err != nil {
		return "", err
	}
	table, err := s.table(ctx, entry.Key.Partition)
	if err != nil {
		return "", err
	}
	if table == "" {
		return "", refuseUnbootstrapped(entry.Key.Partition.Tier)
	}

	if _, err := s.Dynamo.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:                 aws.String(table),
		Item:                      written.item,
		ConditionExpression:       aws.String(written.condition),
		ExpressionAttributeNames:  written.names,
		ExpressionAttributeValues: written.values,
	}); err != nil {
		var failed *ddbtypes.ConditionalCheckFailedException
		if errors.As(err, &failed) {
			return "", keyvalue.ErrStale
		}
		return "", fmt.Errorf("write %s: %w", entry.Key, err)
	}
	return written.revision, nil
}

func (s KeyValues) WritePair(ctx context.Context, first, second keyvalue.Entry) error {
	writes := make([]ddbtypes.TransactWriteItem, 0, 2)
	tables := make([]string, 0, 2)
	for _, entry := range []keyvalue.Entry{first, second} {
		written, err := writingOf(entry)
		if err != nil {
			return err
		}
		table, err := s.table(ctx, entry.Key.Partition)
		if err != nil {
			return err
		}
		if table == "" {
			return refuseUnbootstrapped(entry.Key.Partition.Tier)
		}
		tables = append(tables, table)
		writes = append(writes, ddbtypes.TransactWriteItem{Put: &ddbtypes.Put{
			TableName:                 aws.String(table),
			Item:                      written.item,
			ConditionExpression:       aws.String(written.condition),
			ExpressionAttributeNames:  written.names,
			ExpressionAttributeValues: written.values,
		}})
	}
	if tables[0] != tables[1] {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s and %s are kept in different tables, and one write cannot span both", first.Key, second.Key)
	}

	if _, err := s.Dynamo.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: writes}); err != nil {
		var cancelled *ddbtypes.TransactionCanceledException
		if errors.As(err, &cancelled) && isConditionFailed(cancelled) {
			return keyvalue.ErrStale
		}
		return fmt.Errorf("write %s beside %s: %w", first.Key, second.Key, err)
	}
	return nil
}

func isConditionFailed(cancelled *ddbtypes.TransactionCanceledException) bool {
	for _, reason := range cancelled.CancellationReasons {
		if aws.ToString(reason.Code) == conditionalCheckFailed {
			return true
		}
	}
	return false
}

type writing struct {
	item      map[string]ddbtypes.AttributeValue
	condition string
	names     map[string]string
	values    map[string]ddbtypes.AttributeValue
	revision  keyvalue.Revision
}

func writingOf(entry keyvalue.Entry) (writing, error) {
	if err := keyvalue.RefuseUnwritable(entry); err != nil {
		return writing{}, err
	}
	next, err := keyvalue.NewRevision()
	if err != nil {
		return writing{}, err
	}

	item := pointKey(entry.Key)
	item[valueAttribute] = &ddbtypes.AttributeValueMemberS{Value: string(entry.Value)}
	item[revisionAttribute] = &ddbtypes.AttributeValueMemberS{Value: string(next)}
	written := writing{
		item:      item,
		condition: "attribute_not_exists(#pk)",
		names:     map[string]string{"#pk": partitionAttribute},
		revision:  next,
	}
	if entry.Revision != "" {
		written.condition = "#rev = :rev"
		written.names = map[string]string{"#rev": revisionAttribute}
		written.values = map[string]ddbtypes.AttributeValue{
			":rev": &ddbtypes.AttributeValueMemberS{Value: string(entry.Revision)},
		}
	}
	return written, nil
}

func (s KeyValues) Remove(ctx context.Context, key keyvalue.Key, expected keyvalue.Revision) error {
	if err := keyvalue.RefuseMalformedKey(key); err != nil {
		return err
	}
	table, err := s.table(ctx, key.Partition)
	if err != nil {
		return err
	}
	if table == "" {
		return keyvalue.ErrNotFound
	}
	_, err = s.Dynamo.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName:                           aws.String(table),
		Key:                                 pointKey(key),
		ConditionExpression:                 aws.String("#rev = :rev"),
		ExpressionAttributeNames:            map[string]string{"#rev": revisionAttribute},
		ExpressionAttributeValues:           map[string]ddbtypes.AttributeValue{":rev": &ddbtypes.AttributeValueMemberS{Value: string(expected)}},
		ReturnValuesOnConditionCheckFailure: ddbtypes.ReturnValuesOnConditionCheckFailureAllOld,
	})
	if err == nil {
		return nil
	}
	if isTableMissing(err) {
		return keyvalue.ErrNotFound
	}
	var failed *ddbtypes.ConditionalCheckFailedException
	if errors.As(err, &failed) {
		if len(failed.Item) == 0 {
			return keyvalue.ErrNotFound
		}
		return keyvalue.ErrStale
	}
	return fmt.Errorf("remove %s: %w", key, err)
}

func (s KeyValues) List(ctx context.Context, in keyvalue.Partition, under ...string) ([]keyvalue.Entry, error) {
	if err := keyvalue.RefuseMalformedPartition(in); err != nil {
		return nil, err
	}
	table, err := s.table(ctx, in)
	if err != nil {
		return nil, err
	}
	if table == "" {
		return nil, nil
	}

	condition := "#pk = :pk"
	names := map[string]string{"#pk": partitionAttribute}
	values := map[string]ddbtypes.AttributeValue{":pk": &ddbtypes.AttributeValueMemberS{Value: PartitionKey(in)}}
	if len(under) > 0 {
		condition += " AND begins_with(#sk, :prefix)"
		names["#sk"] = sortAttribute
		values[":prefix"] = &ddbtypes.AttributeValueMemberS{Value: sortKey(under)}
	}

	var out []keyvalue.Entry
	var start map[string]ddbtypes.AttributeValue
	for {
		page, err := s.Dynamo.Query(ctx, &dynamodb.QueryInput{
			TableName:                 aws.String(table),
			KeyConditionExpression:    aws.String(condition),
			ExpressionAttributeNames:  names,
			ExpressionAttributeValues: values,
			ConsistentRead:            aws.Bool(true),
			ExclusiveStartKey:         start,
		})
		if err != nil {
			if isTableMissing(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("read everything in %s: %w", in, err)
		}
		for _, item := range page.Items {
			path, keyed, err := pathOf(item)
			if err != nil {
				return nil, err
			}
			if !keyed {
				continue
			}
			entry, err := entryOf(in.Key(path...), item)
			if err != nil {
				return nil, err
			}
			out = append(out, entry)
		}
		if len(page.LastEvaluatedKey) == 0 {
			return out, nil
		}
		start = page.LastEvaluatedKey
	}
}

func pathOf(item map[string]ddbtypes.AttributeValue) ([]string, bool, error) {
	sk := stringAttribute(item, sortAttribute)
	rest, ok := strings.CutSuffix(sk, segmentSeparator)
	if !ok || rest == "" {
		return nil, false, nil
	}
	path, err := keyvalue.SplitSegments(rest, segmentSeparator)
	return path, err == nil, err
}

func entryOf(key keyvalue.Key, item map[string]ddbtypes.AttributeValue) (keyvalue.Entry, error) {
	value, ok := item[valueAttribute].(*ddbtypes.AttributeValueMemberS)
	if !ok {
		return keyvalue.Entry{}, refusal.Refuse(refusal.CodeNotReady,
			"%s holds an item with no JSON %q attribute, which this build did not write: an older ocel wrote it in a layout this build does not read", key, valueAttribute)
	}
	return keyvalue.Entry{Key: key, Value: []byte(value.Value), Revision: keyvalue.Revision(stringAttribute(item, revisionAttribute))}, nil
}

func join(segments []string) string { return keyvalue.JoinSegments(segments, segmentSeparator, "") }

func pointKey(key keyvalue.Key) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{
		partitionAttribute: &ddbtypes.AttributeValueMemberS{Value: PartitionKey(key.Partition)},
		sortAttribute:      &ddbtypes.AttributeValueMemberS{Value: sortKey(key.Path)},
	}
}

func stringAttribute(item map[string]ddbtypes.AttributeValue, name string) string {
	value, ok := item[name].(*ddbtypes.AttributeValueMemberS)
	if !ok {
		return ""
	}
	return value.Value
}

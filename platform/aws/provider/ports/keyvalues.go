package ports

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/variablestore"
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

var variableRoots = []records.Root{
	variablestore.RootValues,
	variablestore.RootValueRefs,
	envsource.RootRegistrations,
	envsource.RootStatus,
	envsource.RootDigestKey,
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

func (s KeyValues) table(ctx context.Context, in records.Partition) (string, error) {
	if s.Tables == nil {
		return "", nil
	}
	if slices.Contains(variableRoots, in.Root) {
		return s.Tables.ValuesTable(ctx, in.Tier)
	}
	return s.Tables.Table(ctx, in.Tier)
}

func PartitionKey(in records.Partition) string {
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

func (s KeyValues) Read(ctx context.Context, key records.Key) (records.Entry, error) {
	if err := records.RefuseMalformedKey(key); err != nil {
		return records.Entry{}, err
	}
	table, err := s.table(ctx, key.Partition)
	if err != nil {
		return records.Entry{}, err
	}
	if table == "" {
		return records.Entry{}, records.ErrNotFound
	}
	out, err := s.Dynamo.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(table),
		Key:            pointKey(key),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		if isTableMissing(err) {
			return records.Entry{}, records.ErrNotFound
		}
		return records.Entry{}, fmt.Errorf("read %s: %w", key, err)
	}
	if len(out.Item) == 0 {
		return records.Entry{}, records.ErrNotFound
	}
	return entryOf(key, out.Item)
}

func (s KeyValues) Write(ctx context.Context, entry records.Entry) (records.Revision, error) {
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
			return "", records.ErrStale
		}
		return "", fmt.Errorf("write %s: %w", entry.Key, err)
	}
	return written.revision, nil
}

func (s KeyValues) WritePair(ctx context.Context, first, second records.Entry) error {
	writes := make([]ddbtypes.TransactWriteItem, 0, 2)
	tables := make([]string, 0, 2)
	for _, entry := range []records.Entry{first, second} {
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
			return records.ErrStale
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
	revision  records.Revision
}

func writingOf(entry records.Entry) (writing, error) {
	if err := records.RefuseUnwritable(entry); err != nil {
		return writing{}, err
	}
	next, err := records.NewRevision()
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

func (s KeyValues) Remove(ctx context.Context, key records.Key, expected records.Revision) error {
	if err := records.RefuseMalformedKey(key); err != nil {
		return err
	}
	table, err := s.table(ctx, key.Partition)
	if err != nil {
		return err
	}
	if table == "" {
		return records.ErrNotFound
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
		return records.ErrNotFound
	}
	var failed *ddbtypes.ConditionalCheckFailedException
	if errors.As(err, &failed) {
		if len(failed.Item) == 0 {
			return records.ErrNotFound
		}
		return records.ErrStale
	}
	return fmt.Errorf("remove %s: %w", key, err)
}

func (s KeyValues) List(ctx context.Context, in records.Partition, under ...string) ([]records.Entry, error) {
	if err := records.RefuseMalformedPartition(in); err != nil {
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

	var out []records.Entry
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
	path, err := records.SplitSegments(rest, segmentSeparator)
	return path, err == nil, err
}

func entryOf(key records.Key, item map[string]ddbtypes.AttributeValue) (records.Entry, error) {
	value, ok := item[valueAttribute].(*ddbtypes.AttributeValueMemberS)
	if !ok {
		return records.Entry{}, refusal.Refuse(refusal.CodeDenied, "%s holds an item with no JSON %q attribute, so ocel did not write it", key, valueAttribute)
	}
	return records.Entry{Key: key, Value: []byte(value.Value), Revision: records.Revision(stringAttribute(item, revisionAttribute))}, nil
}

func join(segments []string) string { return records.JoinSegments(segments, segmentSeparator, "") }

func pointKey(key records.Key) map[string]ddbtypes.AttributeValue {
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

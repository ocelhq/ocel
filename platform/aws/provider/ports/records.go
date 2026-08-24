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

	"github.com/ocelhq/ocel/pkg/providerkit"
	kit "github.com/ocelhq/ocel/pkg/providerkit/ports"
)

const (
	segmentSeparator = "#"

	partitionAttribute = "pk"
	sortAttribute      = "sk"
	bodyAttribute      = "body"
	revisionAttribute  = "rev"

	conditionalCheckFailed = "ConditionalCheckFailed"
)

var partitionSegments = map[string]int{
	kit.RootValues:      3,
	kit.RootValueRefs:   3,
	kit.RootStacks:      3,
	kit.RootConformance: 3,
	kit.RootLedger:      2,
	kit.RootEdgeStacks:  2,
	kit.RootSchema:      2,
	kit.RootProjects:    2,
	kit.RootBootstrap:   2,
	kit.RootWildcard:    2,
}

type DynamoAPI interface {
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
	Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	TransactWriteItems(context.Context, *dynamodb.TransactWriteItemsInput, ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error)
}

type Records struct {
	Dynamo DynamoAPI
	Tables Tables
}

type Tables interface {
	Table(ctx context.Context, class kit.Class) (string, error)
}

type Table string

func (t Table) Table(context.Context, kit.Class) (string, error) { return string(t), nil }

func (r Records) table(ctx context.Context, name kit.RecordName) (string, error) {
	class, named := providerkit.ClassOf(name)
	if !named {
		return "", kit.Refuse(kit.CodeInvalid,
			"%s names no class, and this account keeps each class's records in the bootstrap that owns them", name)
	}
	if r.Tables == nil {
		return "", nil
	}
	return r.Tables.Table(ctx, class)
}

func Partition(name kit.RecordName) (string, error) {
	pk, _, err := keyOf(name)
	return pk, err
}

func unbootstrapped() error {
	return kit.Refuse(kit.CodeNotReady,
		"this account has no Ocel bootstrap, so there is nowhere to keep a record.\nRun `ocel bootstrap` to create it, then try again")
}

func (r Records) Read(ctx context.Context, name kit.RecordName) (kit.Record, error) {
	table, err := r.table(ctx, name)
	if err != nil {
		return kit.Record{}, err
	}
	if table == "" {
		return kit.Record{}, kit.ErrNoRecord
	}
	pk, sk, err := keyOf(name)
	if err != nil {
		return kit.Record{}, err
	}
	out, err := r.Dynamo.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(table),
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
	table, err := r.table(ctx, record.Name)
	if err != nil {
		return "", err
	}
	if table == "" {
		return "", unbootstrapped()
	}
	written, err := writingOf(record)
	if err != nil {
		return "", err
	}

	if _, err := r.Dynamo.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:                 aws.String(table),
		Item:                      written.item,
		ConditionExpression:       aws.String(written.condition),
		ExpressionAttributeNames:  written.names,
		ExpressionAttributeValues: written.values,
	}); err != nil {
		var failed *ddbtypes.ConditionalCheckFailedException
		if errors.As(err, &failed) {
			return "", kit.ErrStale
		}
		return "", fmt.Errorf("write %s: %w", record.Name, err)
	}
	return written.revision, nil
}

func (r Records) WritePair(ctx context.Context, first, second kit.Record) error {
	table, err := r.table(ctx, first.Name)
	if err != nil {
		return err
	}
	if table == "" {
		return unbootstrapped()
	}
	beside, err := r.table(ctx, second.Name)
	if err != nil {
		return err
	}
	if beside != table {
		return kit.Refuse(kit.CodeInvalid,
			"%s and %s are kept in different tables, and one write cannot span both", first.Name, second.Name)
	}

	writes := make([]ddbtypes.TransactWriteItem, 0, 2)
	for _, record := range []kit.Record{first, second} {
		written, err := writingOf(record)
		if err != nil {
			return err
		}
		writes = append(writes, ddbtypes.TransactWriteItem{Put: &ddbtypes.Put{
			TableName:                 aws.String(table),
			Item:                      written.item,
			ConditionExpression:       aws.String(written.condition),
			ExpressionAttributeNames:  written.names,
			ExpressionAttributeValues: written.values,
		}})
	}

	if _, err := r.Dynamo.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: writes}); err != nil {
		var cancelled *ddbtypes.TransactionCanceledException
		if errors.As(err, &cancelled) && conditionFailed(cancelled) {
			return kit.ErrStale
		}
		return fmt.Errorf("write %s beside %s: %w", first.Name, second.Name, err)
	}
	return nil
}

func conditionFailed(cancelled *ddbtypes.TransactionCanceledException) bool {
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
	revision  kit.Revision
}

func writingOf(record kit.Record) (writing, error) {
	pk, sk, err := keyOf(record.Name)
	if err != nil {
		return writing{}, err
	}
	next, err := mintRevision()
	if err != nil {
		return writing{}, err
	}

	written := writing{
		item: map[string]ddbtypes.AttributeValue{
			partitionAttribute: &ddbtypes.AttributeValueMemberS{Value: pk},
			sortAttribute:      &ddbtypes.AttributeValueMemberS{Value: sk},
			bodyAttribute:      &ddbtypes.AttributeValueMemberB{Value: record.Bytes},
			revisionAttribute:  &ddbtypes.AttributeValueMemberS{Value: string(next)},
		},
		condition: "attribute_not_exists(#pk)",
		names:     map[string]string{"#pk": partitionAttribute},
		revision:  next,
	}
	if record.Revision != "" {
		written.condition = "#rev = :rev"
		written.names["#rev"] = revisionAttribute
		written.values = map[string]ddbtypes.AttributeValue{
			":rev": &ddbtypes.AttributeValueMemberS{Value: string(record.Revision)},
		}
	}
	return written, nil
}

func (r Records) Remove(ctx context.Context, name kit.RecordName, expected kit.Revision) error {
	table, err := r.table(ctx, name)
	if err != nil {
		return err
	}
	if table == "" {
		return kit.ErrNoRecord
	}
	pk, sk, err := keyOf(name)
	if err != nil {
		return err
	}
	_, err = r.Dynamo.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName:                           aws.String(table),
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
	table, err := r.table(ctx, under)
	if err != nil {
		return nil, err
	}
	if table == "" {
		return nil, nil
	}
	pk, prefix, err := prefixOf(under)
	if err != nil {
		return nil, err
	}

	condition := "#pk = :pk"
	names := map[string]string{"#pk": partitionAttribute}
	values := map[string]ddbtypes.AttributeValue{":pk": &ddbtypes.AttributeValueMemberS{Value: pk}}
	if prefix != "" {
		condition += " AND begins_with(#sk, :prefix)"
		names["#sk"] = sortAttribute
		values[":prefix"] = &ddbtypes.AttributeValueMemberS{Value: prefix}
	}

	var out []kit.Record
	var start map[string]ddbtypes.AttributeValue
	for {
		page, err := r.Dynamo.Query(ctx, &dynamodb.QueryInput{
			TableName:                 aws.String(table),
			KeyConditionExpression:    aws.String(condition),
			ExpressionAttributeNames:  names,
			ExpressionAttributeValues: values,
			ConsistentRead:            aws.Bool(true),
			ExclusiveStartKey:         start,
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

func prefixOf(under kit.RecordName) (string, string, error) {
	pk, sk, depth, err := keyed(under)
	if err != nil {
		return "", "", err
	}
	if len(under) == depth {
		return pk, "", nil
	}
	return pk, sk, nil
}

func keyOf(name kit.RecordName) (string, string, error) {
	pk, sk, _, err := keyed(name)
	return pk, sk, err
}

func keyed(name kit.RecordName) (string, string, int, error) {
	if len(name) == 0 {
		return "", "", 0, fmt.Errorf("a record name with no segments names nothing")
	}
	depth, partitioned := partitionSegments[name[0]]
	if !partitioned {
		return "", "", 0, kit.Refuse(kit.CodeInvalid,
			"%s is rooted at %q, and this account partitions no records under that root", name, name[0])
	}
	if len(name) < depth {
		return "", "", 0, fmt.Errorf(
			"%s names %d segments and a %s record partitions on %d: reaching under half a partition would have to walk the whole table",
			name, len(name), name[0], depth)
	}
	return join(name[:depth]), join(name[depth:]) + segmentSeparator, depth, nil
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

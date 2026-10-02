package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	keyPK = "pk"
	keySK = "sk"

	runRetention = 30 * 24 * time.Hour
)

var errConditionFailed = errors.New("the run is not in the state this change needs")

type table interface {
	GetItem(ctx context.Context, in *dynamodb.GetItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(ctx context.Context, in *dynamodb.PutItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	UpdateItem(ctx context.Context, in *dynamodb.UpdateItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	DeleteItem(ctx context.Context, in *dynamodb.DeleteItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
	Query(ctx context.Context, in *dynamodb.QueryInput, optFns ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	TransactWriteItems(ctx context.Context, in *dynamodb.TransactWriteItemsInput, optFns ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error)
}

type store struct {
	db     table
	table  string
	prefix string
	topics []string
}

type runItem struct {
	PK          string   `dynamodbav:"pk"`
	SK          string   `dynamodbav:"sk"`
	Topic       string   `dynamodbav:"topic"`
	Consumer    string   `dynamodbav:"consumer"`
	Status      string   `dynamodbav:"status"`
	Payload     string   `dynamodbav:"payload,omitempty"`
	Output      string   `dynamodbav:"output,omitempty"`
	Error       string   `dynamodbav:"error"`
	Attempts    int      `dynamodbav:"attempts"`
	MaxAttempts int      `dynamodbav:"max_attempts"`
	Tags        []string `dynamodbav:"tags"`
	Metadata    string   `dynamodbav:"metadata,omitempty"`
	CreatedAt   int64    `dynamodbav:"created_at"`
	DueAt       int64    `dynamodbav:"due_at"`
	StartedAt   int64    `dynamodbav:"started_at,omitempty"`
	FinishedAt  int64    `dynamodbav:"finished_at,omitempty"`
	RunExpires  int64    `dynamodbav:"run_expires_at,omitempty"`
	Message     string   `dynamodbav:"message"`
	PublishedAt int64    `dynamodbav:"published_at"`
	Key         string   `dynamodbav:"key"`
	Lane        string   `dynamodbav:"lane"`
	Delivery    string   `dynamodbav:"delivery"`
	Lease       int64    `dynamodbav:"lease_until"`
	Retention   int64    `dynamodbav:"expires_at,omitempty"`
	Revision    string   `dynamodbav:"revision"`
}

func (s store) runPartition(topic string) string { return s.prefix + "run#" + topic }

func (s store) runKey(topic, execution string) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{
		keyPK: &ddbtypes.AttributeValueMemberS{Value: s.runPartition(topic)},
		keySK: &ddbtypes.AttributeValueMemberS{Value: execution},
	}
}

func (s store) pointerKey(execution string) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{
		keyPK: &ddbtypes.AttributeValueMemberS{Value: s.prefix + "exec#" + execution},
		keySK: &ddbtypes.AttributeValueMemberS{Value: "topic"},
	}
}

func (s store) putPointer(execution, topic string, retention any) (ddbtypes.TransactWriteItem, error) {
	expires, err := attributevalue.Marshal(retention)
	if err != nil {
		return ddbtypes.TransactWriteItem{}, err
	}
	item := s.pointerKey(execution)
	item["topic"] = &ddbtypes.AttributeValueMemberS{Value: topic}
	item["expires_at"] = expires
	return ddbtypes.TransactWriteItem{Put: &ddbtypes.Put{TableName: aws.String(s.table), Item: item}}, nil
}

func (s store) topicOf(ctx context.Context, execution string) (string, bool, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(s.table),
		Key:            s.pointerKey(execution),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return "", false, fmt.Errorf("find the topic of run %s: %w", execution, err)
	}
	topic, found := out.Item["topic"].(*ddbtypes.AttributeValueMemberS)
	if !found {
		return "", false, nil
	}
	return topic.Value, true, nil
}

func retentionAfter(dueMicros int64) int64 {
	return time.UnixMicro(dueMicros).Add(runRetention).Unix()
}

func timeOf(us int64) time.Time {
	if us == 0 {
		return time.Time{}
	}
	return time.UnixMicro(us)
}

func (item runItem) run() provider.Run {
	run := provider.Run{
		Execution:  item.SK,
		Topic:      item.Topic,
		Consumer:   item.Consumer,
		Status:     provider.RunStatus(item.Status),
		Error:      item.Error,
		Attempts:   item.Attempts,
		Tags:       item.Tags,
		CreatedAt:  timeOf(item.CreatedAt),
		DueAt:      timeOf(item.DueAt),
		StartedAt:  timeOf(item.StartedAt),
		FinishedAt: timeOf(item.FinishedAt),
		ExpiresAt:  timeOf(item.RunExpires),
		Revision:   keyvalue.Revision(item.Revision),
	}
	if item.Payload != "" {
		run.Payload = json.RawMessage(item.Payload)
	}
	if item.Output != "" {
		run.Output = json.RawMessage(item.Output)
	}
	if item.Metadata != "" {
		run.Metadata = json.RawMessage(item.Metadata)
	}
	return run
}

func (s store) putRun(ctx context.Context, item runItem) error {
	item.PK = s.runPartition(item.Topic)
	if item.Retention == 0 {
		item.Retention = retentionAfter(item.DueAt)
	}
	if item.Tags == nil {
		item.Tags = []string{}
	}
	if item.Revision == "" {
		revision, err := keyvalue.NewRevision()
		if err != nil {
			return err
		}
		item.Revision = string(revision)
	}
	encoded, err := attributevalue.MarshalMap(item)
	if err != nil {
		return err
	}
	pointer, err := s.putPointer(item.SK, item.Topic, item.Retention)
	if err != nil {
		return err
	}
	_, err = s.db.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []ddbtypes.TransactWriteItem{
		{Put: &ddbtypes.Put{TableName: aws.String(s.table), Item: encoded, ConditionExpression: aws.String("attribute_not_exists(pk)")}},
		pointer,
	}})
	if isConditionFailed(err) {
		return errConditionFailed
	}
	if err != nil {
		return fmt.Errorf("record run %s: %w", item.SK, err)
	}
	return nil
}

func (s store) readRun(ctx context.Context, topic, execution string, consistent bool) (runItem, bool, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(s.table),
		Key:            s.runKey(topic, execution),
		ConsistentRead: aws.Bool(consistent),
	})
	if err != nil {
		return runItem{}, false, fmt.Errorf("read run %s: %w", execution, err)
	}
	if len(out.Item) == 0 {
		return runItem{}, false, nil
	}
	var item runItem
	if err := attributevalue.UnmarshalMap(out.Item, &item); err != nil {
		return runItem{}, false, fmt.Errorf("decode run %s: %w", execution, err)
	}
	return item, true, nil
}

type change struct {
	set        map[string]any
	remove     []string
	add        map[string]int
	condition  string
	conditions map[string]any
}

func (s store) updateRun(ctx context.Context, topic, execution string, c change) (runItem, error) {
	names := map[string]string{}
	values := map[string]ddbtypes.AttributeValue{}
	var clauses []string
	n := 0
	name := func(attribute string) string {
		placeholder := fmt.Sprintf("#a%d", len(names))
		names[placeholder] = attribute
		return placeholder
	}
	value := func(v any) (string, error) {
		encoded, err := attributevalue.Marshal(v)
		if err != nil {
			return "", err
		}
		n++
		placeholder := fmt.Sprintf(":v%d", n)
		values[placeholder] = encoded
		return placeholder, nil
	}
	var sets []string
	for attribute, v := range c.set {
		placeholder, err := value(v)
		if err != nil {
			return runItem{}, err
		}
		sets = append(sets, name(attribute)+" = "+placeholder)
	}
	for attribute, by := range c.add {
		placeholder, err := value(by)
		if err != nil {
			return runItem{}, err
		}
		sets = append(sets, name(attribute)+" = "+name(attribute)+" + "+placeholder)
	}
	revision, err := keyvalue.NewRevision()
	if err != nil {
		return runItem{}, err
	}
	placeholder, err := value(string(revision))
	if err != nil {
		return runItem{}, err
	}
	sets = append(sets, name("revision")+" = "+placeholder)
	clauses = append(clauses, "SET "+joinComma(sets))
	var removes []string
	for _, attribute := range c.remove {
		removes = append(removes, name(attribute))
	}
	if len(removes) > 0 {
		clauses = append(clauses, "REMOVE "+joinComma(removes))
	}
	condition := "attribute_exists(pk)"
	if c.condition != "" {
		condition += " AND (" + c.condition + ")"
	}
	for placeholder, v := range c.conditions {
		encoded, err := attributevalue.Marshal(v)
		if err != nil {
			return runItem{}, err
		}
		values[placeholder] = encoded
	}
	for _, attribute := range []string{"status", "delivery", "lease_until", "attempts", "due_at", "consumer", "key", "error", "revision"} {
		names["#"+attribute] = attribute
	}
	expression, names := joinSpace(clauses), usedNames(names, joinSpace(clauses)+" "+condition)
	retention, retained := c.set["expires_at"]
	if !retained {
		out, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
			TableName:                 aws.String(s.table),
			Key:                       s.runKey(topic, execution),
			UpdateExpression:          aws.String(expression),
			ConditionExpression:       aws.String(condition),
			ExpressionAttributeNames:  names,
			ExpressionAttributeValues: values,
			ReturnValues:              ddbtypes.ReturnValueAllNew,
		})
		if isConditionFailed(err) {
			return runItem{}, errConditionFailed
		}
		if err != nil {
			return runItem{}, fmt.Errorf("update run %s: %w", execution, err)
		}
		var item runItem
		if err := attributevalue.UnmarshalMap(out.Attributes, &item); err != nil {
			return runItem{}, fmt.Errorf("decode run %s: %w", execution, err)
		}
		return item, nil
	}
	pointer, err := s.putPointer(execution, topic, retention)
	if err != nil {
		return runItem{}, err
	}
	_, err = s.db.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []ddbtypes.TransactWriteItem{
		{Update: &ddbtypes.Update{
			TableName:                 aws.String(s.table),
			Key:                       s.runKey(topic, execution),
			UpdateExpression:          aws.String(expression),
			ConditionExpression:       aws.String(condition),
			ExpressionAttributeNames:  names,
			ExpressionAttributeValues: values,
		}},
		pointer,
	}})
	if isConditionFailed(err) {
		return runItem{}, errConditionFailed
	}
	if err != nil {
		return runItem{}, fmt.Errorf("update run %s: %w", execution, err)
	}
	item, found, err := s.readRun(ctx, topic, execution, true)
	if err != nil {
		return runItem{}, err
	}
	if !found {
		return runItem{}, errConditionFailed
	}
	return item, nil
}

func (s store) deleteRun(ctx context.Context, topic, execution, condition string, conditions map[string]any) (bool, error) {
	values := map[string]ddbtypes.AttributeValue{}
	for placeholder, v := range conditions {
		encoded, err := attributevalue.Marshal(v)
		if err != nil {
			return false, err
		}
		values[placeholder] = encoded
	}
	deletion := &ddbtypes.Delete{
		TableName: aws.String(s.table),
		Key:       s.runKey(topic, execution),
	}
	if condition != "" {
		deletion.ConditionExpression = aws.String(condition)
		deletion.ExpressionAttributeNames = usedNames(map[string]string{"#status": "status", "#consumer": "consumer"}, condition)
		deletion.ExpressionAttributeValues = values
	}
	_, err := s.db.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []ddbtypes.TransactWriteItem{
		{Delete: deletion},
		{Delete: &ddbtypes.Delete{TableName: aws.String(s.table), Key: s.pointerKey(execution)}},
	}})
	if isConditionFailed(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("delete run %s: %w", execution, err)
	}
	return true, nil
}

type runPage struct {
	items []runItem
	next  map[string]ddbtypes.AttributeValue
}

func (s store) queryRuns(ctx context.Context, topic, before, filter string, filterNames map[string]string, filterValues map[string]any, limit int32, start map[string]ddbtypes.AttributeValue) (runPage, error) {
	values := map[string]ddbtypes.AttributeValue{":pk": &ddbtypes.AttributeValueMemberS{Value: s.runPartition(topic)}}
	keyCondition := "pk = :pk"
	if before != "" {
		values[":before"] = &ddbtypes.AttributeValueMemberS{Value: before}
		keyCondition += " AND sk < :before"
	}
	in := &dynamodb.QueryInput{
		TableName:              aws.String(s.table),
		KeyConditionExpression: aws.String(keyCondition),
		ScanIndexForward:       aws.Bool(false),
		ExclusiveStartKey:      start,
	}
	if limit > 0 {
		in.Limit = aws.Int32(limit)
	}
	if filter != "" {
		in.FilterExpression = aws.String(filter)
		in.ExpressionAttributeNames = filterNames
		for placeholder, v := range filterValues {
			encoded, err := attributevalue.Marshal(v)
			if err != nil {
				return runPage{}, err
			}
			values[placeholder] = encoded
		}
	}
	in.ExpressionAttributeValues = values
	out, err := s.db.Query(ctx, in)
	if err != nil {
		return runPage{}, fmt.Errorf("list the runs of %s: %w", topic, err)
	}
	page := runPage{next: out.LastEvaluatedKey}
	for _, raw := range out.Items {
		var item runItem
		if err := attributevalue.UnmarshalMap(raw, &item); err != nil {
			return runPage{}, fmt.Errorf("decode a run of %s: %w", topic, err)
		}
		page.items = append(page.items, item)
	}
	return page, nil
}

func isConditionFailed(err error) bool {
	var failed *ddbtypes.ConditionalCheckFailedException
	if errors.As(err, &failed) {
		return true
	}
	var canceled *ddbtypes.TransactionCanceledException
	if !errors.As(err, &canceled) {
		return false
	}
	for _, reason := range canceled.CancellationReasons {
		if aws.ToString(reason.Code) == "ConditionalCheckFailed" {
			return true
		}
	}
	return false
}

func joinComma(parts []string) string { return strings.Join(parts, ", ") }

func joinSpace(parts []string) string { return strings.Join(parts, " ") }

func usedNames(names map[string]string, expression string) map[string]string {
	used := map[string]string{}
	for placeholder, attribute := range names {
		if containsPlaceholder(expression, placeholder) {
			used[placeholder] = attribute
		}
	}
	if len(used) == 0 {
		return nil
	}
	return used
}

func containsPlaceholder(expression, placeholder string) bool {
	for from := 0; ; {
		i := strings.Index(expression[from:], placeholder)
		if i < 0 {
			return false
		}
		end := from + i + len(placeholder)
		if end == len(expression) || !isNameCharacter(expression[end]) {
			return true
		}
		from = end
	}
}

func isNameCharacter(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

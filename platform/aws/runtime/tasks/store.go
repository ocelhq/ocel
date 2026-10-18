package tasks

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/runs"
)

const (
	keyPK = "pk"
	keySK = "sk"

	runRetention = 30 * 24 * time.Hour

	smallestQueryPage = 25
	cursorMark        = "before:"
)

type table interface {
	GetItem(ctx context.Context, in *dynamodb.GetItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(ctx context.Context, in *dynamodb.PutItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	UpdateItem(ctx context.Context, in *dynamodb.UpdateItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	DeleteItem(ctx context.Context, in *dynamodb.DeleteItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
	Query(ctx context.Context, in *dynamodb.QueryInput, optFns ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
}

type Store struct {
	db     table
	table  string
	prefix string
}

func NewStore(db table, tableName, prefix string) Store {
	return Store{db: db, table: tableName, prefix: prefix}
}

type runItem struct {
	PK                 string   `dynamodbav:"pk"`
	SK                 string   `dynamodbav:"sk"`
	Topic              string   `dynamodbav:"topic"`
	Consumer           string   `dynamodbav:"consumer"`
	Status             string   `dynamodbav:"status"`
	Payload            string   `dynamodbav:"payload,omitempty"`
	Output             string   `dynamodbav:"output,omitempty"`
	Error              string   `dynamodbav:"error,omitempty"`
	Attempts           int      `dynamodbav:"attempts"`
	MaxAttempts        int      `dynamodbav:"max_attempts"`
	Tags               []string `dynamodbav:"tags,omitempty"`
	Metadata           string   `dynamodbav:"metadata,omitempty"`
	Message            string   `dynamodbav:"message,omitempty"`
	PublishedAtMicros  int64    `dynamodbav:"published_at,omitempty"`
	Key                string   `dynamodbav:"key,omitempty"`
	Lane               string   `dynamodbav:"lane,omitempty"`
	Delivery           string   `dynamodbav:"delivery,omitempty"`
	Held               string   `dynamodbav:"held,omitempty"`
	LeaseUntilMicros   int64    `dynamodbav:"lease_until,omitempty"`
	CreatedAtMicros    int64    `dynamodbav:"created_at"`
	DueAtMicros        int64    `dynamodbav:"due_at,omitempty"`
	StartedAtMicros    int64    `dynamodbav:"started_at,omitempty"`
	FinishedAtMicros   int64    `dynamodbav:"finished_at,omitempty"`
	RunExpiresAtMicros int64    `dynamodbav:"run_expires_at,omitempty"`
	ExpiresAtUnix      int64    `dynamodbav:"expires_at"`
	Revision           string   `dynamodbav:"revision"`
}

func (s Store) runPartition(topic string) string { return s.prefix + "run#" + topic }

func stringKey(pk, sk string) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{
		keyPK: &ddbtypes.AttributeValueMemberS{Value: pk},
		keySK: &ddbtypes.AttributeValueMemberS{Value: sk},
	}
}

func (s Store) itemOf(run runs.Run, revision records.Revision) runItem {
	return runItem{
		PK:                 s.runPartition(run.Topic),
		SK:                 run.Execution,
		Topic:              run.Topic,
		Consumer:           run.Consumer,
		Status:             string(run.Status),
		Payload:            string(run.Payload),
		Output:             string(run.Output),
		Error:              run.Error,
		Attempts:           run.Attempts,
		MaxAttempts:        run.MaxAttempts,
		Tags:               run.Tags,
		Metadata:           string(run.Metadata),
		Message:            run.Message,
		PublishedAtMicros:  microsOf(run.PublishedAt),
		Key:                run.Key,
		Lane:               string(run.Lane),
		Delivery:           run.Delivery,
		Held:               run.Held,
		LeaseUntilMicros:   microsOf(run.LeaseUntil),
		CreatedAtMicros:    microsOf(run.CreatedAt),
		DueAtMicros:        microsOf(run.DueAt),
		StartedAtMicros:    microsOf(run.StartedAt),
		FinishedAtMicros:   microsOf(run.FinishedAt),
		RunExpiresAtMicros: microsOf(run.ExpiresAt),
		ExpiresAtUnix:      purgedAt(run).Unix(),
		Revision:           string(revision),
	}
}

func purgedAt(run runs.Run) time.Time {
	if !run.FinishedAt.IsZero() {
		return run.FinishedAt.Add(runRetention)
	}
	if run.DueAt.After(run.CreatedAt) {
		return run.DueAt.Add(runRetention)
	}
	return run.CreatedAt.Add(runRetention)
}

func (item runItem) run() runs.Run {
	run := runs.Run{
		Execution:   item.SK,
		Topic:       item.Topic,
		Consumer:    item.Consumer,
		Status:      provider.RunStatus(item.Status),
		Error:       item.Error,
		Attempts:    item.Attempts,
		MaxAttempts: item.MaxAttempts,
		Tags:        item.Tags,
		Message:     item.Message,
		PublishedAt: timeOfMicros(item.PublishedAtMicros),
		Key:         item.Key,
		Lane:        provider.Lane(item.Lane),
		Delivery:    item.Delivery,
		Held:        item.Held,
		LeaseUntil:  timeOfMicros(item.LeaseUntilMicros),
		CreatedAt:   timeOfMicros(item.CreatedAtMicros),
		DueAt:       timeOfMicros(item.DueAtMicros),
		StartedAt:   timeOfMicros(item.StartedAtMicros),
		FinishedAt:  timeOfMicros(item.FinishedAtMicros),
		ExpiresAt:   timeOfMicros(item.RunExpiresAtMicros),
		Revision:    records.Revision(item.Revision),
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

func (s Store) CreateRun(ctx context.Context, run runs.Run) (records.Revision, error) {
	return s.putRun(ctx, run, "attribute_not_exists(pk)", nil)
}

func (s Store) WriteRun(ctx context.Context, run runs.Run) (records.Revision, error) {
	if run.Revision == "" {
		return "", fmt.Errorf("run %s carries no revision to write over: %w", run.Execution, records.ErrStale)
	}
	return s.putRun(ctx, run, "#revision = :revision", atRevision(run.Revision))
}

func atRevision(revision records.Revision) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{":revision": &ddbtypes.AttributeValueMemberS{Value: string(revision)}}
}

func (s Store) putRun(ctx context.Context, run runs.Run, condition string, values map[string]ddbtypes.AttributeValue) (records.Revision, error) {
	revision, err := records.NewRevision()
	if err != nil {
		return "", err
	}
	encoded, err := attributevalue.MarshalMap(s.itemOf(run, revision))
	if err != nil {
		return "", fmt.Errorf("encode run %s: %w", run.Execution, err)
	}
	in := &dynamodb.PutItemInput{TableName: aws.String(s.table), Item: encoded, ConditionExpression: aws.String(condition)}
	if values != nil {
		in.ExpressionAttributeNames = map[string]string{"#revision": "revision"}
		in.ExpressionAttributeValues = values
	}
	_, err = s.db.PutItem(ctx, in)
	if isConditionFailed(err) {
		return "", fmt.Errorf("run %s: %w", run.Execution, records.ErrStale)
	}
	if err != nil {
		return "", fmt.Errorf("record run %s: %w", run.Execution, err)
	}
	return revision, nil
}

func (s Store) ReadRun(ctx context.Context, topic, execution string) (runs.Run, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(s.table),
		Key:            stringKey(s.runPartition(topic), execution),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return runs.Run{}, fmt.Errorf("read run %s: %w", execution, err)
	}
	if len(out.Item) == 0 {
		return runs.Run{}, fmt.Errorf("run %s: %w", execution, records.ErrNotFound)
	}
	var item runItem
	if err := attributevalue.UnmarshalMap(out.Item, &item); err != nil {
		return runs.Run{}, fmt.Errorf("decode run %s: %w", execution, err)
	}
	return item.run(), nil
}

func (s Store) DeleteRun(ctx context.Context, run runs.Run) error {
	if run.Revision == "" {
		return fmt.Errorf("run %s carries no revision to delete at: %w", run.Execution, records.ErrStale)
	}
	_, err := s.db.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName:                 aws.String(s.table),
		Key:                       stringKey(s.runPartition(run.Topic), run.Execution),
		ConditionExpression:       aws.String("#revision = :revision"),
		ExpressionAttributeNames:  map[string]string{"#revision": "revision"},
		ExpressionAttributeValues: atRevision(run.Revision),
	})
	if isConditionFailed(err) {
		return fmt.Errorf("run %s: %w", run.Execution, records.ErrStale)
	}
	if err != nil {
		return fmt.Errorf("delete run %s: %w", run.Execution, err)
	}
	return nil
}

func cursorOf(execution string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(cursorMark + execution))
}

func parseCursor(cursor string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	execution, marked := strings.CutPrefix(string(raw), cursorMark)
	if err != nil || !marked || execution == "" {
		return "", fmt.Errorf("cursor %q: %w", cursor, runs.ErrUnknownCursor)
	}
	return execution, nil
}

func (s Store) ListRuns(ctx context.Context, filter runs.RunFilter) (runs.RunPage, error) {
	limit := runs.PageLimit(filter.Limit)
	before := ""
	if filter.Cursor != "" {
		parsed, err := parseCursor(filter.Cursor)
		if err != nil {
			return runs.RunPage{}, err
		}
		before = parsed
	}
	var found []runItem
	for _, topic := range filter.Topics {
		matched, err := s.listMatching(ctx, s.queryOf(topic, before, filter), limit+1)
		if err != nil {
			return runs.RunPage{}, err
		}
		found = append(found, matched...)
	}
	slices.SortFunc(found, func(a, b runItem) int { return strings.Compare(b.SK, a.SK) })
	var page runs.RunPage
	if len(found) > limit {
		found = found[:limit]
		page.NextCursor = cursorOf(found[limit-1].SK)
	}
	for _, item := range found {
		page.Runs = append(page.Runs, item.run())
	}
	return page, nil
}

func (s Store) CountRuns(ctx context.Context, filter runs.RunFilter) (int64, error) {
	var count int64
	for _, topic := range filter.Topics {
		in := s.queryOf(topic, "", filter)
		in.Select = ddbtypes.SelectCount
		for {
			out, err := s.db.Query(ctx, in)
			if err != nil {
				return 0, fmt.Errorf("count the runs of %s: %w", topic, err)
			}
			count += int64(out.Count)
			if len(out.LastEvaluatedKey) == 0 {
				break
			}
			in.ExclusiveStartKey = out.LastEvaluatedKey
		}
	}
	return count, nil
}

func (s Store) queryOf(topic, before string, filter runs.RunFilter) *dynamodb.QueryInput {
	names := map[string]string{}
	values := map[string]ddbtypes.AttributeValue{":pk": &ddbtypes.AttributeValueMemberS{Value: s.runPartition(topic)}}
	keyCondition := "pk = :pk"
	if before != "" {
		values[":before"] = &ddbtypes.AttributeValueMemberS{Value: before}
		keyCondition += " AND sk < :before"
	}
	var clauses []string
	if filter.Consumer != "" {
		names["#consumer"] = "consumer"
		values[":consumer"] = &ddbtypes.AttributeValueMemberS{Value: filter.Consumer}
		clauses = append(clauses, "#consumer = :consumer")
	}
	if len(filter.Statuses) > 0 {
		names["#status"] = "status"
		placeholders := make([]string, 0, len(filter.Statuses))
		for i, status := range filter.Statuses {
			placeholder := fmt.Sprintf(":status%d", i)
			values[placeholder] = &ddbtypes.AttributeValueMemberS{Value: string(status)}
			placeholders = append(placeholders, placeholder)
		}
		clauses = append(clauses, "#status IN ("+strings.Join(placeholders, ", ")+")")
	}
	for i, tag := range filter.Tags {
		placeholder := fmt.Sprintf(":tag%d", i)
		names["#tags"] = "tags"
		values[placeholder] = &ddbtypes.AttributeValueMemberS{Value: tag}
		clauses = append(clauses, "contains(#tags, "+placeholder+")")
	}
	in := &dynamodb.QueryInput{
		TableName:                 aws.String(s.table),
		KeyConditionExpression:    aws.String(keyCondition),
		ScanIndexForward:          aws.Bool(false),
		ExpressionAttributeValues: values,
	}
	if len(clauses) > 0 {
		in.FilterExpression = aws.String(strings.Join(clauses, " AND "))
		in.ExpressionAttributeNames = names
	}
	return in
}

func (s Store) listMatching(ctx context.Context, in *dynamodb.QueryInput, want int) ([]runItem, error) {
	var matched []runItem
	for {
		in.Limit = aws.Int32(int32(max(want-len(matched), smallestQueryPage)))
		out, err := s.db.Query(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("list runs: %w", err)
		}
		for _, raw := range out.Items {
			var item runItem
			if err := attributevalue.UnmarshalMap(raw, &item); err != nil {
				return nil, fmt.Errorf("decode a listed run: %w", err)
			}
			matched = append(matched, item)
		}
		if len(matched) >= want || len(out.LastEvaluatedKey) == 0 {
			break
		}
		in.ExclusiveStartKey = out.LastEvaluatedKey
	}
	if len(matched) > want {
		matched = matched[:want]
	}
	return matched, nil
}

func isConditionFailed(err error) bool {
	var failed *ddbtypes.ConditionalCheckFailedException
	return errors.As(err, &failed)
}

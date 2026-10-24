package topics

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/runs"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const (
	storeCollection        = "taskstores"
	environmentsCollection = "environments"
	runsCollection         = "runs"
	recordsCollection      = "records"

	maxRunScan = 1000
	countAlias = "n"

	RunRetention = 30 * 24 * time.Hour
)

var _ runs.Store = Store{}

type Scope struct {
	Slug        string
	Tier        environment.Tier
	Environment string
}

func projectDocument(client *firestore.Client, tier environment.Tier, slug string) *firestore.DocumentRef {
	return client.Collection(storeCollection).Doc(string(tier) + "~" + slug)
}

type Store struct {
	Clients *ports.Clients
	Scope   Scope
}

type recordDocument struct {
	Value     string    `firestore:"value"`
	ExpiresAt time.Time `firestore:"expiresAt"`
}

type runDocument struct {
	Topic       string     `firestore:"topic"`
	Consumer    string     `firestore:"consumer"`
	Status      string     `firestore:"status"`
	Payload     *string    `firestore:"payload"`
	Output      *string    `firestore:"output"`
	Error       string     `firestore:"error"`
	Attempts    int        `firestore:"attempts"`
	MaxAttempts int        `firestore:"maxAttempts"`
	Tags        []string   `firestore:"tags"`
	Metadata    *string    `firestore:"metadata"`
	Message     string     `firestore:"message"`
	PublishedAt *time.Time `firestore:"publishedAt"`
	Key         string     `firestore:"key"`
	Lane        string     `firestore:"lane"`
	Delivery    string     `firestore:"delivery"`
	Held        string     `firestore:"held"`
	LeaseUntil  *time.Time `firestore:"leaseUntil"`
	CreatedAt   time.Time  `firestore:"createdAt"`
	DueAt       *time.Time `firestore:"dueAt"`
	StartedAt   *time.Time `firestore:"startedAt"`
	FinishedAt  *time.Time `firestore:"finishedAt"`
	ExpiresAt   *time.Time `firestore:"expiresAt"`
	PurgeAt     time.Time  `firestore:"purgeAt"`
	Revision    string     `firestore:"revision"`
}

func (s Store) client(ctx context.Context) (*firestore.Client, error) {
	return s.Clients.ProjectFirestore(ctx, s.Scope.Tier, s.Scope.Slug)
}

func (s Store) scopeDocument(ctx context.Context) (*firestore.DocumentRef, error) {
	client, err := s.client(ctx)
	if err != nil {
		return nil, err
	}
	return projectDocument(client, s.Scope.Tier, s.Scope.Slug).Collection(environmentsCollection).Doc(s.Scope.Environment), nil
}

func (s Store) runDocument(ctx context.Context, execution string) (*firestore.DocumentRef, error) {
	scope, err := s.scopeDocument(ctx)
	if err != nil {
		return nil, err
	}
	return scope.Collection(runsCollection).Doc(execution), nil
}

func recordID(purpose provider.RecordPurpose, topic, key string) string {
	return string(purpose) + "~" + topic + "~" + base64.RawURLEncoding.EncodeToString([]byte(key))
}

func (s Store) recordDocument(ctx context.Context, record provider.ExpiringRecord) (*firestore.DocumentRef, error) {
	scope, err := s.scopeDocument(ctx)
	if err != nil {
		return nil, err
	}
	return scope.Collection(recordsCollection).Doc(recordID(record.Purpose, record.Topic, record.Key)), nil
}

func (s Store) EnsureRecord(ctx context.Context, record provider.ExpiringRecord) (provider.ExpiringRecord, bool, error) {
	doc, err := s.recordDocument(ctx, record)
	if err != nil {
		return provider.ExpiringRecord{}, false, err
	}
	written := recordDocument{Value: string(record.Value), ExpiresAt: record.ExpiresAt}
	var kept provider.ExpiringRecord
	var created bool
	err = records.RetryWhileStale(ctx, func() (bool, error) {
		kept, created = record, true
		_, err := doc.Create(ctx, written)
		if status.Code(err) != codes.AlreadyExists {
			return false, err
		}
		snapshot, err := doc.Get(ctx)
		if isNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		var current recordDocument
		if err := snapshot.DataTo(&current); err != nil {
			return false, err
		}
		if current.ExpiresAt.After(time.Now()) {
			kept.Value, kept.ExpiresAt, created = json.RawMessage(current.Value), current.ExpiresAt, false
			return false, nil
		}
		_, err = doc.Update(ctx, []firestore.Update{
			{Path: "value", Value: written.Value},
			{Path: "expiresAt", Value: written.ExpiresAt},
		}, firestore.LastUpdateTime(snapshot.UpdateTime))
		if status.Code(err) == codes.FailedPrecondition || isNotFound(err) {
			return true, nil
		}
		return false, err
	})
	if err != nil {
		return provider.ExpiringRecord{}, false, fmt.Errorf("ensure the %s record %q of %s: %w", record.Purpose, record.Key, record.Topic, err)
	}
	return kept, created, nil
}

func (s Store) ReplaceRecord(ctx context.Context, record provider.ExpiringRecord, held json.RawMessage) error {
	doc, err := s.recordDocument(ctx, record)
	if err != nil {
		return err
	}
	err = replaceRecordHeld(ctx, doc, record, held)
	if errors.Is(err, records.ErrStale) {
		return fmt.Errorf("the %s record %q of %s: %w", record.Purpose, record.Key, record.Topic, records.ErrStale)
	}
	if err != nil {
		return fmt.Errorf("replace the %s record %q of %s: %w", record.Purpose, record.Key, record.Topic, err)
	}
	return nil
}

func replaceRecordHeld(ctx context.Context, doc *firestore.DocumentRef, record provider.ExpiringRecord, held json.RawMessage) error {
	snapshot, current, found, err := readRecord(ctx, doc)
	if err != nil {
		return err
	}
	if !found || current.Value != string(held) {
		return records.ErrStale
	}
	_, err = doc.Update(ctx, []firestore.Update{
		{Path: "value", Value: string(record.Value)},
		{Path: "expiresAt", Value: record.ExpiresAt},
	}, firestore.LastUpdateTime(snapshot.UpdateTime))
	if changedSince(err) {
		return records.ErrStale
	}
	return err
}

func (s Store) DeleteRecord(ctx context.Context, record provider.ExpiringRecord) error {
	doc, err := s.recordDocument(ctx, record)
	if err != nil {
		return err
	}
	err = records.RetryWhileStale(ctx, func() (bool, error) {
		snapshot, current, found, err := readRecord(ctx, doc)
		if err != nil || !found || current.Value != string(record.Value) {
			return false, err
		}
		_, err = doc.Delete(ctx, firestore.LastUpdateTime(snapshot.UpdateTime))
		if changedSince(err) {
			return true, nil
		}
		return false, err
	})
	if err != nil {
		return fmt.Errorf("delete the %s record %q of %s: %w", record.Purpose, record.Key, record.Topic, err)
	}
	return nil
}

func readRecord(ctx context.Context, doc *firestore.DocumentRef) (*firestore.DocumentSnapshot, recordDocument, bool, error) {
	snapshot, err := doc.Get(ctx)
	if isNotFound(err) {
		return nil, recordDocument{}, false, nil
	}
	if err != nil {
		return nil, recordDocument{}, false, err
	}
	var current recordDocument
	if err := snapshot.DataTo(&current); err != nil {
		return nil, recordDocument{}, false, err
	}
	return snapshot, current, true, nil
}

func changedSince(err error) bool {
	return status.Code(err) == codes.FailedPrecondition || isNotFound(err)
}

func isNotFound(err error) bool { return status.Code(err) == codes.NotFound }

func (s Store) CreateRun(ctx context.Context, run runs.Run) (records.Revision, error) {
	doc, err := s.runDocument(ctx, run.Execution)
	if err != nil {
		return "", err
	}
	revision, err := records.NewRevision()
	if err != nil {
		return "", err
	}
	_, err = doc.Create(ctx, documentOf(run, revision))
	if status.Code(err) == codes.AlreadyExists {
		return "", records.ErrStale
	}
	if err != nil {
		return "", fmt.Errorf("record run %s: %w", run.Execution, err)
	}
	return revision, nil
}

func (s Store) ReadRun(ctx context.Context, topic, execution string) (runs.Run, error) {
	doc, err := s.runDocument(ctx, execution)
	if err != nil {
		return runs.Run{}, err
	}
	snapshot, err := doc.Get(ctx)
	if isNotFound(err) {
		return runs.Run{}, records.ErrNotFound
	}
	if err != nil {
		return runs.Run{}, fmt.Errorf("read run %s: %w", execution, err)
	}
	run, err := runOf(snapshot)
	if err != nil {
		return runs.Run{}, err
	}
	if run.Topic != topic {
		return runs.Run{}, records.ErrNotFound
	}
	return run, nil
}

func (s Store) WriteRun(ctx context.Context, run runs.Run) (records.Revision, error) {
	if run.Revision == "" {
		return "", records.ErrStale
	}
	next, err := records.NewRevision()
	if err != nil {
		return "", err
	}
	err = s.atRevision(ctx, run, func(doc *firestore.DocumentRef, unchanged firestore.Precondition) error {
		_, err := doc.Update(ctx, fieldsOf(documentOf(run, next)), unchanged)
		return err
	})
	if errors.Is(err, records.ErrStale) {
		return "", records.ErrStale
	}
	if err != nil {
		return "", fmt.Errorf("write run %s: %w", run.Execution, err)
	}
	return next, nil
}

func (s Store) DeleteRun(ctx context.Context, run runs.Run) error {
	if run.Revision == "" {
		return records.ErrStale
	}
	err := s.atRevision(ctx, run, func(doc *firestore.DocumentRef, unchanged firestore.Precondition) error {
		_, err := doc.Delete(ctx, unchanged)
		return err
	})
	if errors.Is(err, records.ErrStale) {
		return records.ErrStale
	}
	if err != nil {
		return fmt.Errorf("delete run %s: %w", run.Execution, err)
	}
	return nil
}

func (s Store) atRevision(ctx context.Context, run runs.Run, change func(*firestore.DocumentRef, firestore.Precondition) error) error {
	doc, err := s.runDocument(ctx, run.Execution)
	if err != nil {
		return err
	}
	snapshot, err := doc.Get(ctx)
	if isNotFound(err) {
		return records.ErrStale
	}
	if err != nil {
		return err
	}
	current, err := runOf(snapshot)
	if err != nil {
		return err
	}
	if current.Topic != run.Topic || current.Revision != run.Revision {
		return records.ErrStale
	}
	if err := change(doc, firestore.LastUpdateTime(snapshot.UpdateTime)); changedSince(err) {
		return records.ErrStale
	} else if err != nil {
		return err
	}
	return nil
}

func fieldsOf(doc runDocument) []firestore.Update {
	value := reflect.ValueOf(doc)
	fields := make([]firestore.Update, 0, value.NumField())
	for i := range value.NumField() {
		fields = append(fields, firestore.Update{Path: value.Type().Field(i).Tag.Get("firestore"), Value: value.Field(i).Interface()})
	}
	return fields
}

func runOf(snapshot *firestore.DocumentSnapshot) (runs.Run, error) {
	var doc runDocument
	if err := snapshot.DataTo(&doc); err != nil {
		return runs.Run{}, fmt.Errorf("decode run %s: %w", snapshot.Ref.ID, err)
	}
	return runs.Run{
		Execution:   snapshot.Ref.ID,
		Topic:       doc.Topic,
		Consumer:    doc.Consumer,
		Status:      provider.RunStatus(doc.Status),
		Payload:     rawOf(doc.Payload),
		Output:      rawOf(doc.Output),
		Error:       doc.Error,
		Attempts:    doc.Attempts,
		MaxAttempts: doc.MaxAttempts,
		Tags:        doc.Tags,
		Metadata:    rawOf(doc.Metadata),
		Message:     doc.Message,
		PublishedAt: timeOf(doc.PublishedAt),
		Key:         doc.Key,
		Lane:        provider.Lane(doc.Lane),
		Delivery:    doc.Delivery,
		Held:        doc.Held,
		LeaseUntil:  timeOf(doc.LeaseUntil),
		CreatedAt:   doc.CreatedAt,
		DueAt:       timeOf(doc.DueAt),
		StartedAt:   timeOf(doc.StartedAt),
		FinishedAt:  timeOf(doc.FinishedAt),
		ExpiresAt:   timeOf(doc.ExpiresAt),
		Revision:    records.Revision(doc.Revision),
	}, nil
}

func documentOf(run runs.Run, revision records.Revision) runDocument {
	doc := runDocument{
		Topic:       run.Topic,
		Consumer:    run.Consumer,
		Status:      string(run.Status),
		Payload:     textOf(run.Payload),
		Output:      textOf(run.Output),
		Error:       run.Error,
		Attempts:    run.Attempts,
		MaxAttempts: run.MaxAttempts,
		Tags:        run.Tags,
		Metadata:    textOf(run.Metadata),
		Message:     run.Message,
		PublishedAt: instantOf(run.PublishedAt),
		Key:         run.Key,
		Lane:        string(run.Lane),
		Delivery:    run.Delivery,
		Held:        run.Held,
		LeaseUntil:  instantOf(run.LeaseUntil),
		CreatedAt:   run.CreatedAt,
		DueAt:       instantOf(run.DueAt),
		StartedAt:   instantOf(run.StartedAt),
		FinishedAt:  instantOf(run.FinishedAt),
		ExpiresAt:   instantOf(run.ExpiresAt),
		PurgeAt:     purgeAtOf(run),
		Revision:    string(revision),
	}
	if doc.Tags == nil {
		doc.Tags = []string{}
	}
	return doc
}

func purgeAtOf(run runs.Run) time.Time {
	if !runs.IsUnfinished(run.Status) && !run.FinishedAt.IsZero() {
		return run.FinishedAt.Add(RunRetention)
	}
	return maxTime(run.DueAt, run.CreatedAt).Add(RunRetention)
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (s Store) queriesOf(ctx context.Context, filter runs.RunFilter) ([]firestore.Query, error) {
	scope, err := s.scopeDocument(ctx)
	if err != nil {
		return nil, err
	}
	collection := scope.Collection(runsCollection)
	queries := make([]firestore.Query, 0, len(filter.Topics))
	for _, topic := range slices.Compact(slices.Sorted(slices.Values(filter.Topics))) {
		query := collection.Where("topic", "==", topic)
		if filter.Consumer != "" {
			query = query.Where("consumer", "==", filter.Consumer)
		}
		if len(filter.Statuses) > 0 {
			statuses := make([]string, len(filter.Statuses))
			for i, status := range filter.Statuses {
				statuses[i] = string(status)
			}
			query = query.Where("status", "in", statuses)
		}
		if len(filter.Tags) > 0 {
			query = query.Where("tags", "array-contains", filter.Tags[0])
		}
		queries = append(queries, query)
	}
	return queries, nil
}

type topicListing struct {
	documents *firestore.DocumentIterator
	scanLimit int
	tags      []string
	scanned   int
	head      *runs.Run
	bounded   bool
	capped    bool
}

func (l *topicListing) advance() error {
	l.head = nil
	for {
		snapshot, err := l.documents.Next()
		if errors.Is(err, iterator.Done) {
			l.capped = l.scanned == l.scanLimit
			return nil
		}
		if err != nil {
			return fmt.Errorf("list runs: %w", err)
		}
		l.scanned++
		run, err := runOf(snapshot)
		if err != nil {
			return err
		}
		if holdsEveryTag(run.Tags, l.tags) {
			l.head = &run
			return nil
		}
		if l.scanned == l.scanLimit {
			l.head, l.bounded = &run, true
			return nil
		}
	}
}

func (s Store) ListRuns(ctx context.Context, filter runs.RunFilter) (runs.RunPage, error) {
	limit := runs.PageLimit(filter.Limit)
	var after runs.Run
	if filter.Cursor != "" {
		created, execution, err := runs.ParseCursor(filter.Cursor)
		if err != nil {
			return runs.RunPage{}, err
		}
		if execution == "" {
			return runs.RunPage{}, fmt.Errorf("cursor %q names no run: %w", filter.Cursor, runs.ErrUnknownCursor)
		}
		after = runs.Run{CreatedAt: created, Execution: execution}
	}
	queries, err := s.queriesOf(ctx, filter)
	if err != nil {
		return runs.RunPage{}, err
	}
	scanLimit := limit + 1
	if len(filter.Tags) > 1 {
		scanLimit = max(maxRunScan, scanLimit)
	}
	listings := make([]*topicListing, 0, len(queries))
	defer func() {
		for _, listing := range listings {
			listing.documents.Stop()
		}
	}()
	for _, query := range queries {
		query = query.OrderBy("createdAt", firestore.Desc).OrderBy(firestore.DocumentID, firestore.Desc)
		if filter.Cursor != "" {
			query = query.StartAfter(after.CreatedAt, after.Execution)
		}
		listing := &topicListing{documents: query.Limit(scanLimit).Documents(ctx), scanLimit: scanLimit, tags: filter.Tags}
		listings = append(listings, listing)
		if err := listing.advance(); err != nil {
			return runs.RunPage{}, err
		}
	}
	var page runs.RunPage
	for {
		next := newestHead(listings)
		switch {
		case next == nil:
			return page, nil
		case len(page.Runs) == limit:
			last := page.Runs[limit-1]
			page.NextCursor = runs.CursorOf(last.CreatedAt, last.Execution)
			return page, nil
		case next.bounded:
			page.NextCursor = runs.CursorOf(next.head.CreatedAt, next.head.Execution)
			return page, nil
		}
		run := *next.head
		page.Runs = append(page.Runs, run)
		if err := next.advance(); err != nil {
			return runs.RunPage{}, err
		}
		if next.head == nil && next.capped {
			page.NextCursor = runs.CursorOf(run.CreatedAt, run.Execution)
			return page, nil
		}
	}
}

func newestHead(listings []*topicListing) *topicListing {
	var newest *topicListing
	for _, listing := range listings {
		if listing.head == nil {
			continue
		}
		if newest == nil || newerThan(*listing.head, *newest.head) {
			newest = listing
		}
	}
	return newest
}

func newerThan(a, b runs.Run) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	return a.Execution > b.Execution
}

func (s Store) CountRuns(ctx context.Context, filter runs.RunFilter) (int64, error) {
	queries, err := s.queriesOf(ctx, filter)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, query := range queries {
		counted, err := countOf(ctx, query, filter.Tags)
		if err != nil {
			return 0, fmt.Errorf("count runs: %w", err)
		}
		total += counted
	}
	return total, nil
}

func countOf(ctx context.Context, query firestore.Query, tags []string) (int64, error) {
	if len(tags) <= 1 {
		result, err := query.NewAggregationQuery().WithCount(countAlias).Get(ctx)
		if err != nil {
			return 0, err
		}
		var counted struct {
			N int64 `firestore:"n"`
		}
		if err := result.DataTo(&counted); err != nil {
			return 0, err
		}
		return counted.N, nil
	}
	documents := query.Select("tags").Documents(ctx)
	defer documents.Stop()
	var counted int64
	for {
		snapshot, err := documents.Next()
		if errors.Is(err, iterator.Done) {
			return counted, nil
		}
		if err != nil {
			return 0, err
		}
		var tagged struct {
			Tags []string `firestore:"tags"`
		}
		if err := snapshot.DataTo(&tagged); err != nil {
			return 0, err
		}
		if holdsEveryTag(tagged.Tags, tags) {
			counted++
		}
	}
}

func holdsEveryTag(tags, wanted []string) bool {
	for _, tag := range wanted {
		if !slices.Contains(tags, tag) {
			return false
		}
	}
	return true
}

func textOf(value json.RawMessage) *string {
	if len(value) == 0 {
		return nil
	}
	text := string(value)
	return &text
}

func rawOf(text *string) json.RawMessage {
	if text == nil {
		return nil
	}
	return json.RawMessage(*text)
}

func instantOf(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func timeOf(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

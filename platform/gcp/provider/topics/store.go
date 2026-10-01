package topics

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const (
	storeCollection   = "taskstores"
	runsCollection    = "runs"
	recordsCollection = "records"

	defaultRunPage = 100
	maxRunPage     = 1000
	maxRunScan     = 1000

	RunRetention = 30 * 24 * time.Hour
)

var ErrUnknownCursor = errors.New("the cursor is not one a listing returned")

type Scope struct {
	Slug        string
	Tier        environment.Tier
	Environment string
}

func (s Scope) documentID() string {
	return string(s.Tier) + "~" + s.Slug + "~" + s.Environment
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
	Topic      string     `firestore:"topic"`
	Consumer   string     `firestore:"consumer"`
	Status     string     `firestore:"status"`
	Payload    *string    `firestore:"payload"`
	Output     *string    `firestore:"output"`
	Error      string     `firestore:"error"`
	Attempts   int        `firestore:"attempts"`
	Tags       []string   `firestore:"tags"`
	Metadata   *string    `firestore:"metadata"`
	CreatedAt  time.Time  `firestore:"createdAt"`
	DueAt      *time.Time `firestore:"dueAt"`
	StartedAt  *time.Time `firestore:"startedAt"`
	FinishedAt *time.Time `firestore:"finishedAt"`
	ExpiresAt  *time.Time `firestore:"expiresAt"`
	PurgeAt    *time.Time `firestore:"purgeAt"`
	Revision   string     `firestore:"revision"`

	Delivery deliveryFields `firestore:"delivery"`
}

type deliveryFields struct {
	MessageID   string     `firestore:"messageId"`
	PublishedAt *time.Time `firestore:"publishedAt"`
	MaxAttempts int        `firestore:"maxAttempts"`
	Key         string     `firestore:"key"`
	Lane        string     `firestore:"lane"`
	DelayTask   string     `firestore:"delayTask"`
}

type storedRun struct {
	provider.Run
	delivery deliveryFields
}

func (s Store) scopeDocument() (*firestore.DocumentRef, error) {
	client, err := s.Clients.Firestore()
	if err != nil {
		return nil, err
	}
	return client.Collection(storeCollection).Doc(s.Scope.documentID()), nil
}

func (s Store) runs() (*firestore.CollectionRef, error) {
	scope, err := s.scopeDocument()
	if err != nil {
		return nil, err
	}
	return scope.Collection(runsCollection), nil
}

func recordID(purpose provider.RecordPurpose, topic, key string) string {
	return string(purpose) + "~" + topic + "~" + base64.RawURLEncoding.EncodeToString([]byte(key))
}

func (s Store) EnsureRecord(ctx context.Context, record provider.ExpiringRecord) (provider.ExpiringRecord, bool, error) {
	doc, err := s.recordDocument(record)
	if err != nil {
		return provider.ExpiringRecord{}, false, err
	}
	client, err := s.Clients.Firestore()
	if err != nil {
		return provider.ExpiringRecord{}, false, err
	}
	kept := record
	var created bool
	err = client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, found, err := readInTransaction(tx, doc)
		if err != nil {
			return err
		}
		if found {
			var current recordDocument
			if err := snapshot.DataTo(&current); err != nil {
				return err
			}
			if current.ExpiresAt.After(time.Now()) {
				kept.Value, kept.ExpiresAt, created = json.RawMessage(current.Value), current.ExpiresAt, false
				return nil
			}
		}
		kept, created = record, true
		return tx.Set(doc, recordDocument{Value: string(record.Value), ExpiresAt: record.ExpiresAt})
	})
	if err != nil {
		return provider.ExpiringRecord{}, false, fmt.Errorf("ensure the %s record %q of %s: %w", record.Purpose, record.Key, record.Topic, err)
	}
	return kept, created, nil
}

func (s Store) recordDocument(record provider.ExpiringRecord) (*firestore.DocumentRef, error) {
	scope, err := s.scopeDocument()
	if err != nil {
		return nil, err
	}
	return scope.Collection(recordsCollection).Doc(recordID(record.Purpose, record.Topic, record.Key)), nil
}

func (s Store) writeRecord(ctx context.Context, record provider.ExpiringRecord) error {
	doc, err := s.recordDocument(record)
	if err != nil {
		return err
	}
	if _, err := doc.Set(ctx, recordDocument{Value: string(record.Value), ExpiresAt: record.ExpiresAt}); err != nil {
		return fmt.Errorf("write the %s record %q of %s: %w", record.Purpose, record.Key, record.Topic, err)
	}
	return nil
}

func (s Store) changeRecordHolding(ctx context.Context, record provider.ExpiringRecord, change func(*firestore.Transaction, *firestore.DocumentRef) error) error {
	doc, err := s.recordDocument(record)
	if err != nil {
		return err
	}
	client, err := s.Clients.Firestore()
	if err != nil {
		return err
	}
	return client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, found, err := readInTransaction(tx, doc)
		if err != nil || !found {
			return err
		}
		var current recordDocument
		if err := snapshot.DataTo(&current); err != nil {
			return err
		}
		if current.Value != string(record.Value) {
			return nil
		}
		return change(tx, doc)
	})
}

func (s Store) deleteRecordHolding(ctx context.Context, record provider.ExpiringRecord) error {
	err := s.changeRecordHolding(ctx, record, func(tx *firestore.Transaction, doc *firestore.DocumentRef) error {
		return tx.Delete(doc)
	})
	if err != nil {
		return fmt.Errorf("delete the %s record %q of %s: %w", record.Purpose, record.Key, record.Topic, err)
	}
	return nil
}

func (s Store) extendRecordHolding(ctx context.Context, record provider.ExpiringRecord) error {
	err := s.changeRecordHolding(ctx, record, func(tx *firestore.Transaction, doc *firestore.DocumentRef) error {
		return tx.Update(doc, []firestore.Update{{Path: "expiresAt", Value: record.ExpiresAt}})
	})
	if err != nil {
		return fmt.Errorf("extend the %s record %q of %s: %w", record.Purpose, record.Key, record.Topic, err)
	}
	return nil
}

func (s Store) ensureDebouncedRun(ctx context.Context, record provider.ExpiringRecord, run storedRun) (string, error) {
	recordDoc, err := s.recordDocument(record)
	if err != nil {
		return "", err
	}
	runs, err := s.runs()
	if err != nil {
		return "", err
	}
	client, err := s.Clients.Firestore()
	if err != nil {
		return "", err
	}
	next, err := keyvalue.NewRevision()
	if err != nil {
		return "", err
	}
	var pending string
	err = client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		recorded, err := readPendingRun(tx, recordDoc, runs)
		if err != nil || recorded != "" {
			pending = recorded
			return err
		}
		pending = run.Execution
		if err := tx.Set(recordDoc, recordDocument{Value: string(record.Value), ExpiresAt: record.ExpiresAt}); err != nil {
			return err
		}
		return tx.Create(runs.Doc(run.Execution), documentOf(run.Run, run.delivery, next))
	})
	if err != nil {
		return "", fmt.Errorf("record the debounced run %s under %q of %s: %w", run.Execution, record.Key, record.Topic, err)
	}
	return pending, nil
}

func readPendingRun(tx *firestore.Transaction, recordDoc *firestore.DocumentRef, runs *firestore.CollectionRef) (string, error) {
	snapshot, found, err := readInTransaction(tx, recordDoc)
	if err != nil || !found {
		return "", err
	}
	var current recordDocument
	if err := snapshot.DataTo(&current); err != nil {
		return "", err
	}
	now := time.Now()
	if !current.ExpiresAt.After(now) {
		return "", nil
	}
	recorded, err := readRecordedRun(provider.ExpiringRecord{Value: json.RawMessage(current.Value)})
	if err != nil {
		return "", err
	}
	runSnapshot, found, err := readInTransaction(tx, runs.Doc(recorded))
	if err != nil || !found {
		return "", err
	}
	pending, err := storedRunOf(runSnapshot)
	if err != nil || refuseUnreschedulable(pending, now) != nil {
		return "", err
	}
	return recorded, nil
}

func (s Store) deleteRecordsHolding(ctx context.Context, records ...provider.ExpiringRecord) error {
	var errs []error
	for _, record := range records {
		if record.Key != "" {
			errs = append(errs, s.deleteRecordHolding(ctx, record))
		}
	}
	return errors.Join(errs...)
}

func (s Store) pointRecordAt(ctx context.Context, record provider.ExpiringRecord, execution string) error {
	if record.Key == "" {
		return nil
	}
	return s.writeRecord(ctx, newRecordNamingRun(record.Purpose, record.Topic, record.Key, execution, record.ExpiresAt))
}

func readInTransaction(tx *firestore.Transaction, doc *firestore.DocumentRef) (*firestore.DocumentSnapshot, bool, error) {
	snapshot, err := tx.Get(doc)
	if isNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return snapshot, snapshot.Exists(), nil
}

func (s Store) ReadRun(ctx context.Context, execution string) (provider.Run, error) {
	run, err := s.readStoredRun(ctx, execution)
	return run.Run, err
}

func isNotFound(err error) bool { return status.Code(err) == codes.NotFound }

func runOf(snapshot *firestore.DocumentSnapshot) (provider.Run, error) {
	run, err := storedRunOf(snapshot)
	return run.Run, err
}

func storedRunOf(snapshot *firestore.DocumentSnapshot) (storedRun, error) {
	var doc runDocument
	if err := snapshot.DataTo(&doc); err != nil {
		return storedRun{}, fmt.Errorf("decode run %s: %w", snapshot.Ref.ID, err)
	}
	return storedRun{delivery: doc.Delivery, Run: provider.Run{
		Execution:  snapshot.Ref.ID,
		Topic:      doc.Topic,
		Consumer:   doc.Consumer,
		Status:     provider.RunStatus(doc.Status),
		Payload:    rawOf(doc.Payload),
		Output:     rawOf(doc.Output),
		Error:      doc.Error,
		Attempts:   doc.Attempts,
		Tags:       doc.Tags,
		Metadata:   rawOf(doc.Metadata),
		CreatedAt:  doc.CreatedAt,
		DueAt:      timeOf(doc.DueAt),
		StartedAt:  timeOf(doc.StartedAt),
		FinishedAt: timeOf(doc.FinishedAt),
		ExpiresAt:  timeOf(doc.ExpiresAt),
		Revision:   keyvalue.Revision(doc.Revision),
	}}, nil
}

func documentOf(run provider.Run, delivery deliveryFields, revision keyvalue.Revision) runDocument {
	doc := runDocument{
		Delivery:   delivery,
		Topic:      run.Topic,
		Consumer:   run.Consumer,
		Status:     string(run.Status),
		Payload:    textOf(run.Payload),
		Output:     textOf(run.Output),
		Error:      run.Error,
		Attempts:   run.Attempts,
		Tags:       run.Tags,
		Metadata:   textOf(run.Metadata),
		CreatedAt:  run.CreatedAt,
		DueAt:      instantOf(run.DueAt),
		StartedAt:  instantOf(run.StartedAt),
		FinishedAt: instantOf(run.FinishedAt),
		ExpiresAt:  instantOf(run.ExpiresAt),
		Revision:   string(revision),
	}
	if doc.Tags == nil {
		doc.Tags = []string{}
	}
	if !run.FinishedAt.IsZero() {
		doc.PurgeAt = instantOf(run.FinishedAt.Add(RunRetention))
	}
	return doc
}

func (s Store) WriteRun(ctx context.Context, run provider.Run) (keyvalue.Revision, error) {
	runs, err := s.runs()
	if err != nil {
		return "", err
	}
	client, err := s.Clients.Firestore()
	if err != nil {
		return "", err
	}
	next, err := keyvalue.NewRevision()
	if err != nil {
		return "", err
	}
	doc := runs.Doc(run.Execution)
	err = client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, found, err := readInTransaction(tx, doc)
		if err != nil {
			return err
		}
		if !found {
			if run.Revision != "" {
				return keyvalue.ErrStale
			}
			return tx.Create(doc, documentOf(run, deliveryFields{}, next))
		}
		current, err := storedRunOf(snapshot)
		if err != nil {
			return err
		}
		if run.Revision == "" || current.Revision != run.Revision {
			return keyvalue.ErrStale
		}
		return tx.Set(doc, documentOf(run, current.delivery, next))
	})
	if errors.Is(err, keyvalue.ErrStale) {
		return "", keyvalue.ErrStale
	}
	if err != nil {
		return "", fmt.Errorf("write run %s: %w", run.Execution, err)
	}
	return next, nil
}

var errRunUnchanged = errors.New("the run is left as it was")

func (s Store) changeRun(ctx context.Context, execution string, change func(run *storedRun, found bool) error) (storedRun, error) {
	runs, err := s.runs()
	if err != nil {
		return storedRun{}, err
	}
	client, err := s.Clients.Firestore()
	if err != nil {
		return storedRun{}, err
	}
	doc := runs.Doc(execution)
	var changed storedRun
	err = client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, found, err := readInTransaction(tx, doc)
		if err != nil {
			return err
		}
		changed = storedRun{Run: provider.Run{Execution: execution}}
		if found {
			if changed, err = storedRunOf(snapshot); err != nil {
				return err
			}
		}
		if err := change(&changed, found); err != nil {
			return err
		}
		next, err := keyvalue.NewRevision()
		if err != nil {
			return err
		}
		changed.Revision = next
		return tx.Set(doc, documentOf(changed.Run, changed.delivery, next))
	})
	if err != nil {
		return changed, err
	}
	return changed, nil
}

var errRunExists = errors.New("a run with this execution is already recorded")

func (s Store) createRun(ctx context.Context, run storedRun) error {
	_, err := s.changeRun(ctx, run.Execution, func(current *storedRun, found bool) error {
		if found {
			return errRunExists
		}
		*current = run
		return nil
	})
	if err != nil {
		return fmt.Errorf("record run %s: %w", run.Execution, err)
	}
	return nil
}

func (s Store) ListRuns(ctx context.Context, filter provider.RunFilter) (provider.RunPage, error) {
	runs, err := s.runs()
	if err != nil {
		return provider.RunPage{}, err
	}
	limit := pageLimit(filter.Limit)
	query := runs.Query
	if filter.Topic != "" {
		query = query.Where("topic", "==", filter.Topic)
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
	query = query.OrderBy("createdAt", firestore.Desc).OrderBy(firestore.DocumentID, firestore.Desc)
	if filter.Cursor != "" {
		created, execution, err := parseCursor(filter.Cursor)
		if err != nil {
			return provider.RunPage{}, err
		}
		query = query.StartAfter(created, execution)
	}
	scanLimit := limit + 1
	if len(filter.Tags) > 1 {
		scanLimit = max(maxRunScan, scanLimit)
	}
	documents := query.Limit(scanLimit).Documents(ctx)
	defer documents.Stop()
	var page provider.RunPage
	var scanned int
	var last provider.Run
	for {
		snapshot, err := documents.Next()
		if errors.Is(err, iterator.Done) {
			if scanned == scanLimit {
				page.NextCursor = cursorOf(last.CreatedAt, last.Execution)
			}
			return page, nil
		}
		if err != nil {
			return provider.RunPage{}, fmt.Errorf("list runs: %w", err)
		}
		run, err := runOf(snapshot)
		if err != nil {
			return provider.RunPage{}, err
		}
		scanned++
		if !holdsEveryTag(run.Tags, filter.Tags) {
			last = run
			continue
		}
		if len(page.Runs) == limit {
			page.NextCursor = cursorOf(last.CreatedAt, last.Execution)
			return page, nil
		}
		page.Runs = append(page.Runs, run)
		last = run
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

func pageLimit(requested int) int {
	if requested <= 0 {
		return defaultRunPage
	}
	return min(requested, maxRunPage)
}

func cursorOf(created time.Time, execution string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(created.UTC().Format(time.RFC3339Nano) + "/" + execution))
}

func parseCursor(cursor string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err == nil {
		at, execution, cut := strings.Cut(string(raw), "/")
		if created, parseErr := time.Parse(time.RFC3339Nano, at); cut && parseErr == nil && execution != "" {
			return created, execution, nil
		}
	}
	return time.Time{}, "", fmt.Errorf("cursor %q: %w", cursor, ErrUnknownCursor)
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

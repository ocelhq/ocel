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
	scope, err := s.scopeDocument()
	if err != nil {
		return provider.ExpiringRecord{}, false, err
	}
	client, err := s.Clients.Firestore()
	if err != nil {
		return provider.ExpiringRecord{}, false, err
	}
	doc := scope.Collection(recordsCollection).Doc(recordID(record.Purpose, record.Topic, record.Key))
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

func readInTransaction(tx *firestore.Transaction, doc *firestore.DocumentRef) (*firestore.DocumentSnapshot, bool, error) {
	snapshot, err := tx.Get(doc)
	if status.Code(err) == codes.NotFound {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return snapshot, snapshot.Exists(), nil
}

func (s Store) ReadRun(ctx context.Context, execution string) (provider.Run, error) {
	runs, err := s.runs()
	if err != nil {
		return provider.Run{}, err
	}
	snapshot, err := runs.Doc(execution).Get(ctx)
	if status.Code(err) == codes.NotFound || (err == nil && !snapshot.Exists()) {
		return provider.Run{}, keyvalue.ErrNotFound
	}
	if err != nil {
		return provider.Run{}, fmt.Errorf("read run %s: %w", execution, err)
	}
	return runOf(snapshot)
}

func runOf(snapshot *firestore.DocumentSnapshot) (provider.Run, error) {
	var doc runDocument
	if err := snapshot.DataTo(&doc); err != nil {
		return provider.Run{}, fmt.Errorf("decode run %s: %w", snapshot.Ref.ID, err)
	}
	return provider.Run{
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
	}, nil
}

func documentOf(run provider.Run, revision keyvalue.Revision) runDocument {
	doc := runDocument{
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
			return tx.Create(doc, documentOf(run, next))
		}
		current, err := snapshot.DataAt("revision")
		if err != nil || run.Revision == "" || current != string(run.Revision) {
			return keyvalue.ErrStale
		}
		return tx.Set(doc, documentOf(run, next))
	})
	if errors.Is(err, keyvalue.ErrStale) {
		return "", keyvalue.ErrStale
	}
	if err != nil {
		return "", fmt.Errorf("write run %s: %w", run.Execution, err)
	}
	return next, nil
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
	documents := query.Documents(ctx)
	defer documents.Stop()
	var page provider.RunPage
	for {
		snapshot, err := documents.Next()
		if errors.Is(err, iterator.Done) {
			return page, nil
		}
		if err != nil {
			return provider.RunPage{}, fmt.Errorf("list runs: %w", err)
		}
		run, err := runOf(snapshot)
		if err != nil {
			return provider.RunPage{}, err
		}
		if !holdsEveryTag(run.Tags, filter.Tags) {
			continue
		}
		if len(page.Runs) == limit {
			last := page.Runs[limit-1]
			page.NextCursor = cursorOf(last.CreatedAt, last.Execution)
			return page, nil
		}
		page.Runs = append(page.Runs, run)
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

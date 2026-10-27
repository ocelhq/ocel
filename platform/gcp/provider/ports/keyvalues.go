package ports

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
)

const (
	tierCollectionPrefix = "keyvalues-"
	segmentSeparator     = "#"
	segmentCeiling       = "$"
	keySeparator         = "|"
	keyCeiling           = "}"
	valueField           = "value"
	revisionField        = "rev"
)

const tierDatabasePatience = 3 * time.Minute

type KeyValues struct {
	Clients *Clients
}

func (s KeyValues) open(ctx context.Context, in records.Partition) (*firestore.Client, *firestore.CollectionRef, error) {
	if err := records.RefuseMalformedPartition(in); err != nil {
		return nil, nil, err
	}
	client, err := s.client(ctx, in)
	if err != nil {
		return nil, nil, err
	}
	return client, OpenTierCollection(client, in.Tier), nil
}

func (s KeyValues) client(ctx context.Context, in records.Partition) (*firestore.Client, error) {
	if project, owned := ProjectOfRecords(in); owned {
		return s.Clients.ProjectFirestore(ctx, in.Tier, project)
	}
	return s.Clients.TierFirestore(in.Tier)
}

func OpenTierCollection(client *firestore.Client, tier environment.Tier) *firestore.CollectionRef {
	return client.Collection(tierCollectionPrefix + string(tier))
}

func (s KeyValues) refuseAbsentDocument(ctx context.Context, collection *firestore.CollectionRef, key records.Key) error {
	documents := googleretry.QueryDocuments(ctx, collection.Select().Limit(1))
	defer documents.Stop()
	_, err := documents.Next()
	if status.Code(err) == codes.NotFound || googleretry.IsRetried(err) {
		return fmt.Errorf("read %s: %w", key, err)
	}
	return records.ErrNotFound
}

func untilTierServes[T any](ctx context.Context, s KeyValues, in records.Partition, call func() (T, error)) (T, error) {
	if _, owned := ProjectOfRecords(in); owned {
		return call()
	}
	answer, err := call()
	if !googleretry.IsUnserved(err) {
		return answer, err
	}
	var nothing T
	database := s.Clients.RecordsDatabase(in.Tier)
	held, heldErr := s.Clients.hasDatabase(ctx, database)
	switch {
	case heldErr != nil:
		return nothing, fmt.Errorf("the %q Firestore database answered as one that does not exist, then %w", database, heldErr)
	case !held:
		return nothing, s.refuseUnbootstrapped(in.Tier)
	}
	err = googleretry.UntilServedWithin(ctx, tierDatabasePatience, func() error {
		answer, err = call()
		return err
	})
	if googleretry.IsUnserved(err) {
		return nothing, refusal.Refuse(refusal.CodeNotReady,
			"Firestore holds the %q database but answered for %s that it does not exist.\nRun the same command again in a few minutes",
			database, tierDatabasePatience)
	}
	return answer, err
}

func (c *Clients) hasDatabase(ctx context.Context, database string) (bool, error) {
	service, err := c.Databases()
	if err != nil {
		return false, err
	}
	held, err := googleretry.Attempted(ctx, service.Projects.Databases.Get("projects/"+c.Project+"/databases/"+database).Context(ctx).Do)
	if googleretry.AnsweredCode(err) == http.StatusNotFound {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the %q Firestore database: %w", database, err)
	}
	return held.DeleteTime == "", nil
}

func (s KeyValues) Read(ctx context.Context, key records.Key) (records.Entry, error) {
	if err := records.RefuseMalformedKey(key); err != nil {
		return records.Entry{}, err
	}
	return untilTierServes(ctx, s, key.Partition, func() (records.Entry, error) { return s.read(ctx, key) })
}

func (s KeyValues) read(ctx context.Context, key records.Key) (records.Entry, error) {
	_, collection, err := s.open(ctx, key.Partition)
	if err != nil {
		return records.Entry{}, err
	}
	snapshot, err := collection.Doc(documentID(key)).Get(ctx)
	if _, owned := ProjectOfRecords(key.Partition); owned && status.Code(err) == codes.NotFound {
		return records.Entry{}, records.ErrNotFound
	}
	if status.Code(err) == codes.NotFound {
		return records.Entry{}, s.refuseAbsentDocument(ctx, collection, key)
	}
	if err != nil {
		return records.Entry{}, fmt.Errorf("read %s: %w", key, err)
	}
	return entryOf(key, snapshot)
}

func (s KeyValues) Write(ctx context.Context, entry records.Entry) (records.Revision, error) {
	if err := records.RefuseUnwritable(entry); err != nil {
		return "", err
	}
	return untilTierServes(ctx, s, entry.Key.Partition, func() (records.Revision, error) { return s.write(ctx, entry) })
}

func (s KeyValues) write(ctx context.Context, entry records.Entry) (records.Revision, error) {
	client, collection, err := s.open(ctx, entry.Key.Partition)
	if err != nil {
		return "", err
	}
	next, err := records.NewRevision()
	if err != nil {
		return "", err
	}
	err = client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		return compareAndSet(tx, collection, entry, next)
	})
	if err != nil {
		return "", s.refuseFailedWrite(entry.Key, err)
	}
	return next, nil
}

func (s KeyValues) WritePair(ctx context.Context, first, second records.Entry) error {
	for _, entry := range []records.Entry{first, second} {
		if err := records.RefuseUnwritable(entry); err != nil {
			return err
		}
	}
	_, err := untilTierServes(ctx, s, first.Key.Partition, func() (struct{}, error) { return struct{}{}, s.writePair(ctx, first, second) })
	return err
}

func (s KeyValues) writePair(ctx context.Context, first, second records.Entry) error {
	client, collection, err := s.open(ctx, first.Key.Partition)
	if err != nil {
		return err
	}
	_, beside, err := s.open(ctx, second.Key.Partition)
	if err != nil {
		return err
	}
	if beside.Path != collection.Path {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s and %s are kept in different collections, and one transaction cannot span both", first.Key, second.Key)
	}
	revisions := make([]records.Revision, 2)
	for slot := range revisions {
		if revisions[slot], err = records.NewRevision(); err != nil {
			return err
		}
	}
	err = client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		pair := []records.Entry{first, second}
		for _, entry := range pair {
			if err := refuseMovedRevision(tx, collection, entry); err != nil {
				return err
			}
		}
		for slot, entry := range pair {
			if err := setEntry(tx, collection, entry, revisions[slot]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return s.refuseFailedWrite(first.Key, err)
	}
	return nil
}

func (s KeyValues) Remove(ctx context.Context, key records.Key, expected records.Revision) error {
	if err := records.RefuseMalformedKey(key); err != nil {
		return err
	}
	_, err := untilTierServes(ctx, s, key.Partition, func() (struct{}, error) { return struct{}{}, s.remove(ctx, key, expected) })
	return err
}

func (s KeyValues) remove(ctx context.Context, key records.Key, expected records.Revision) error {
	client, collection, err := s.open(ctx, key.Partition)
	if err != nil {
		return err
	}
	err = client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		reference := collection.Doc(documentID(key))
		current, exists, err := currentRevision(tx, reference)
		if err != nil {
			return err
		}
		if !exists {
			return records.ErrNotFound
		}
		if current != expected {
			return records.ErrStale
		}
		return tx.Delete(reference)
	})
	if err != nil {
		if _, owned := ProjectOfRecords(key.Partition); owned && status.Code(err) == codes.NotFound {
			return records.ErrNotFound
		}
		return s.refuseFailedWrite(key, err)
	}
	return nil
}

func (s KeyValues) List(ctx context.Context, in records.Partition, under ...string) ([]records.Entry, error) {
	return untilTierServes(ctx, s, in, func() ([]records.Entry, error) { return s.list(ctx, in, under...) })
}

func (s KeyValues) list(ctx context.Context, in records.Partition, under ...string) ([]records.Entry, error) {
	_, collection, err := s.open(ctx, in)
	if err != nil {
		return nil, err
	}
	partition := partitionPrefix(in)
	at, beneath, ceiling := partition, partition, partitionCeiling(in)
	if len(under) > 0 {
		at = partition + join(under)
		beneath = at + segmentSeparator
		ceiling = at + segmentCeiling
	}

	found, err := googleretry.Called(ctx, func() ([]*firestore.DocumentSnapshot, error) {
		return collection.
			OrderBy(firestore.DocumentID, firestore.Asc).
			StartAt(at).
			EndBefore(ceiling).
			Documents(ctx).
			GetAll()
	})
	if _, owned := ProjectOfRecords(in); owned && status.Code(err) == codes.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read everything in %s: %w", in, err)
	}
	var entries []records.Entry
	for _, snapshot := range found {
		id := snapshot.Ref.ID
		if id != at && !strings.HasPrefix(id, beneath) {
			continue
		}
		path, keyed := strings.CutPrefix(id, partition)
		if !keyed || path == "" {
			continue
		}
		segments, err := records.SplitSegments(path, segmentSeparator)
		if err != nil {
			return nil, err
		}
		entry, err := entryOf(in.Key(segments...), snapshot)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func compareAndSet(tx *firestore.Transaction, collection *firestore.CollectionRef, entry records.Entry, next records.Revision) error {
	if err := refuseMovedRevision(tx, collection, entry); err != nil {
		return err
	}
	return setEntry(tx, collection, entry, next)
}

func refuseMovedRevision(tx *firestore.Transaction, collection *firestore.CollectionRef, entry records.Entry) error {
	current, exists, err := currentRevision(tx, collection.Doc(documentID(entry.Key)))
	if err != nil {
		return err
	}
	if entry.Revision == "" {
		if exists {
			return records.ErrStale
		}
		return nil
	}
	if !exists || current != entry.Revision {
		return records.ErrStale
	}
	return nil
}

func setEntry(tx *firestore.Transaction, collection *firestore.CollectionRef, entry records.Entry, next records.Revision) error {
	return tx.Set(collection.Doc(documentID(entry.Key)), map[string]any{
		valueField:    string(entry.Value),
		revisionField: string(next),
	})
}

func currentRevision(tx *firestore.Transaction, reference *firestore.DocumentRef) (records.Revision, bool, error) {
	snapshot, err := tx.Get(reference)
	if status.Code(err) == codes.NotFound {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !snapshot.Exists() {
		return "", false, nil
	}
	return revisionOf(snapshot), true, nil
}

func (s KeyValues) refuseFailedWrite(key records.Key, err error) error {
	if errors.Is(err, records.ErrStale) || errors.Is(err, records.ErrNotFound) {
		return err
	}
	if project, owned := ProjectOfRecords(key.Partition); owned && status.Code(err) == codes.NotFound {
		return refuseNoProjectRecords(key.Partition.Tier, project, err)
	}
	return fmt.Errorf("write %s: %w", key, err)
}

var ErrNoRecordsDatabase = errors.New("the tier keeps no records database")

type noRecordsDatabase struct{ refused error }

func (e noRecordsDatabase) Error() string { return e.refused.Error() }

func (e noRecordsDatabase) Unwrap() []error { return []error{e.refused, ErrNoRecordsDatabase} }

func (s KeyValues) refuseUnbootstrapped(tier environment.Tier) error {
	return noRecordsDatabase{refused: refusal.Refuse(refusal.CodeNotReady,
		"this project keeps no %q Firestore database, so there is nowhere to store an entry.\nRun `%s` to create it, then try again",
		s.Clients.RecordsDatabase(tier), provider.BootstrapCommand(tier))}
}

func entryOf(key records.Key, snapshot *firestore.DocumentSnapshot) (records.Entry, error) {
	value, present := snapshot.Data()[valueField].(string)
	if !present {
		return records.Entry{}, refusal.Refuse(refusal.CodeDenied, "%s holds a document with no JSON %q field, so ocel did not write it", key, valueField)
	}
	return records.Entry{Key: key, Value: []byte(value), Revision: revisionOf(snapshot)}, nil
}

func revisionOf(snapshot *firestore.DocumentSnapshot) records.Revision {
	revision, _ := snapshot.Data()[revisionField].(string)
	return records.Revision(revision)
}

func partitionPrefix(in records.Partition) string {
	return join(in.Segments()) + keySeparator
}

func partitionCeiling(in records.Partition) string {
	return join(in.Segments()) + keyCeiling
}

func documentID(key records.Key) string {
	return partitionPrefix(key.Partition) + join(key.Path)
}

func join(segments []string) string {
	return records.JoinSegments(segments, segmentSeparator, "/"+keySeparator)
}

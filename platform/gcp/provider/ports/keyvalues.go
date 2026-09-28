package ports

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	tierCollectionPrefix = "records-"
	segmentSeparator     = "#"
	segmentCeiling       = "$"
	keySeparator         = "|"
	keyCeiling           = "}"
	valueField           = "value"
	revisionField        = "rev"
)

type KeyValues struct {
	Clients *Clients
}

func (s KeyValues) collection(in keyvalue.Partition) (*firestore.CollectionRef, error) {
	if err := keyvalue.RefuseMalformedPartition(in); err != nil {
		return nil, err
	}
	client, err := s.Clients.Firestore()
	if err != nil {
		return nil, err
	}
	return OpenTierCollection(client, in.Tier), nil
}

func OpenTierCollection(client *firestore.Client, tier environment.Tier) *firestore.CollectionRef {
	return client.Collection(tierCollectionPrefix + string(tier))
}

func (s KeyValues) refuseAbsentDocument(ctx context.Context, collection *firestore.CollectionRef, key keyvalue.Key) error {
	flat := flatDocumentID(key)
	documents := collection.Select().OrderBy(firestore.DocumentID, firestore.Asc).StartAt(flat).Limit(1).Documents(ctx)
	defer documents.Stop()
	snapshot, err := documents.Next()
	switch {
	case status.Code(err) == codes.NotFound:
		return s.refuseUnbootstrapped(key.Partition.Tier)
	case err == nil && snapshot.Ref.ID == flat:
		return refusal.Refuse(refusal.CodeNotReady,
			"%s is kept as the document %q, which this build did not write: an older ocel wrote it in a layout this build does not read", key, flat)
	}
	return keyvalue.ErrNotFound
}

func (s KeyValues) Read(ctx context.Context, key keyvalue.Key) (keyvalue.Entry, error) {
	if err := keyvalue.RefuseMalformedKey(key); err != nil {
		return keyvalue.Entry{}, err
	}
	collection, err := s.collection(key.Partition)
	if err != nil {
		return keyvalue.Entry{}, err
	}
	snapshot, err := collection.Doc(documentID(key)).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return keyvalue.Entry{}, s.refuseAbsentDocument(ctx, collection, key)
	}
	if err != nil {
		return keyvalue.Entry{}, fmt.Errorf("read %s: %w", key, err)
	}
	return entryOf(key, snapshot)
}

func (s KeyValues) Write(ctx context.Context, entry keyvalue.Entry) (keyvalue.Revision, error) {
	if err := refuseUnwritable(entry); err != nil {
		return "", err
	}
	collection, err := s.collection(entry.Key.Partition)
	if err != nil {
		return "", err
	}
	next, err := keyvalue.NewRevision()
	if err != nil {
		return "", err
	}
	client, err := s.Clients.Firestore()
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

func (s KeyValues) WritePair(ctx context.Context, first, second keyvalue.Entry) error {
	for _, entry := range []keyvalue.Entry{first, second} {
		if err := refuseUnwritable(entry); err != nil {
			return err
		}
	}
	collection, err := s.collection(first.Key.Partition)
	if err != nil {
		return err
	}
	beside, err := s.collection(second.Key.Partition)
	if err != nil {
		return err
	}
	if beside.Path != collection.Path {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s and %s are kept in different collections, and one transaction cannot span both", first.Key, second.Key)
	}
	revisions := make([]keyvalue.Revision, 2)
	for slot := range revisions {
		if revisions[slot], err = keyvalue.NewRevision(); err != nil {
			return err
		}
	}
	client, err := s.Clients.Firestore()
	if err != nil {
		return err
	}
	err = client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		pair := []keyvalue.Entry{first, second}
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

func refuseUnwritable(entry keyvalue.Entry) error {
	if err := keyvalue.RefuseMalformedKey(entry.Key); err != nil {
		return err
	}
	return keyvalue.RefuseNonJSON(entry)
}

func (s KeyValues) Remove(ctx context.Context, key keyvalue.Key, expected keyvalue.Revision) error {
	if err := keyvalue.RefuseMalformedKey(key); err != nil {
		return err
	}
	collection, err := s.collection(key.Partition)
	if err != nil {
		return err
	}
	client, err := s.Clients.Firestore()
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
			return keyvalue.ErrNotFound
		}
		if current != expected {
			return keyvalue.ErrStale
		}
		return tx.Delete(reference)
	})
	if err != nil {
		return s.refuseFailedWrite(key, err)
	}
	return nil
}

func (s KeyValues) List(ctx context.Context, in keyvalue.Partition, under ...string) ([]keyvalue.Entry, error) {
	collection, err := s.collection(in)
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

	var found []keyvalue.Entry
	documents := collection.
		OrderBy(firestore.DocumentID, firestore.Asc).
		StartAt(at).
		EndBefore(ceiling).
		Documents(ctx)
	defer documents.Stop()
	for {
		snapshot, err := documents.Next()
		if errors.Is(err, iterator.Done) {
			return found, nil
		}
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return nil, s.refuseUnbootstrapped(in.Tier)
			}
			return nil, fmt.Errorf("read everything in %s: %w", in, err)
		}
		id := snapshot.Ref.ID
		if id != at && !strings.HasPrefix(id, beneath) {
			continue
		}
		path, keyed := strings.CutPrefix(id, partition)
		if !keyed || path == "" {
			continue
		}
		entry, err := entryOf(in.Key(keyvalue.SplitSegments(path, segmentSeparator)...), snapshot)
		if err != nil {
			return nil, err
		}
		found = append(found, entry)
	}
}

func compareAndSet(tx *firestore.Transaction, collection *firestore.CollectionRef, entry keyvalue.Entry, next keyvalue.Revision) error {
	if err := refuseMovedRevision(tx, collection, entry); err != nil {
		return err
	}
	return setEntry(tx, collection, entry, next)
}

func refuseMovedRevision(tx *firestore.Transaction, collection *firestore.CollectionRef, entry keyvalue.Entry) error {
	current, exists, err := currentRevision(tx, collection.Doc(documentID(entry.Key)))
	if err != nil {
		return err
	}
	if entry.Revision == "" {
		if exists {
			return keyvalue.ErrStale
		}
		return nil
	}
	if !exists || current != entry.Revision {
		return keyvalue.ErrStale
	}
	return nil
}

func setEntry(tx *firestore.Transaction, collection *firestore.CollectionRef, entry keyvalue.Entry, next keyvalue.Revision) error {
	return tx.Set(collection.Doc(documentID(entry.Key)), map[string]any{
		valueField:    string(entry.Value),
		revisionField: string(next),
	})
}

func currentRevision(tx *firestore.Transaction, reference *firestore.DocumentRef) (keyvalue.Revision, bool, error) {
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

func (s KeyValues) refuseFailedWrite(key keyvalue.Key, err error) error {
	if errors.Is(err, keyvalue.ErrStale) || errors.Is(err, keyvalue.ErrNotFound) {
		return err
	}
	if status.Code(err) == codes.NotFound {
		return s.refuseUnbootstrapped(key.Partition.Tier)
	}
	return fmt.Errorf("write %s: %w", key, err)
}

func (s KeyValues) refuseUnbootstrapped(tier environment.Tier) error {
	return refusal.Refuse(refusal.CodeNotReady,
		"this project keeps no %q Firestore database, so there is nowhere to store an entry.\nRun `%s` to create it, then try again",
		s.Clients.Database(), provider.BootstrapCommand(tier))
}

func entryOf(key keyvalue.Key, snapshot *firestore.DocumentSnapshot) (keyvalue.Entry, error) {
	value, present := snapshot.Data()[valueField].(string)
	if !present {
		return keyvalue.Entry{}, refusal.Refuse(refusal.CodeNotReady,
			"%s holds a document with no JSON %q field, which this build did not write: an older ocel wrote it in a layout this build does not read", key, valueField)
	}
	return keyvalue.Entry{Key: key, Value: []byte(value), Revision: revisionOf(snapshot)}, nil
}

func revisionOf(snapshot *firestore.DocumentSnapshot) keyvalue.Revision {
	revision, _ := snapshot.Data()[revisionField].(string)
	return keyvalue.Revision(revision)
}

func partitionPrefix(in keyvalue.Partition) string {
	return join(in.Segments()) + keySeparator
}

func partitionCeiling(in keyvalue.Partition) string {
	return join(in.Segments()) + keyCeiling
}

func documentID(key keyvalue.Key) string {
	return partitionPrefix(key.Partition) + join(key.Path)
}

func flatDocumentID(key keyvalue.Key) string {
	return keyvalue.JoinSegments(append(key.Partition.Segments(), key.Path...), segmentSeparator, "/")
}

func join(segments []string) string {
	return keyvalue.JoinSegments(segments, segmentSeparator, "/"+keySeparator)
}

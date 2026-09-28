package ports

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	tierCollection    = "records-"
	segmentSeparator  = "#"
	segmentCeiling    = "$"
	keySeparator      = "|"
	keyCeiling        = "}"
	valueField        = "value"
	revisionField     = "rev"
	revisionTokenSize = 16
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
	return TierKeyValues(client, in.Tier), nil
}

func TierKeyValues(client *firestore.Client, tier environment.Tier) *firestore.CollectionRef {
	return client.Collection(tierCollection + string(tier))
}

func (s KeyValues) absent(ctx context.Context, collection *firestore.CollectionRef, key keyvalue.Key) error {
	older := olderLayoutID(key)
	documents := collection.Select().OrderBy(firestore.DocumentID, firestore.Asc).StartAt(older).Limit(1).Documents(ctx)
	defer documents.Stop()
	snapshot, err := documents.Next()
	switch {
	case status.Code(err) == codes.NotFound:
		return s.unbootstrapped(key.Partition.Tier)
	case err == nil && snapshot.Ref.ID == older:
		return refusal.Refuse(refusal.CodeNotReady,
			"%s is kept as the document %q, which this build did not write: an older ocel wrote it in a layout this build does not read", key, older)
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
		return keyvalue.Entry{}, s.absent(ctx, collection, key)
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
	next, err := mintRevision()
	if err != nil {
		return "", err
	}
	client, err := s.Clients.Firestore()
	if err != nil {
		return "", err
	}
	err = client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		return writeInto(tx, collection, entry, next)
	})
	if err != nil {
		return "", s.writeFailed(entry.Key, err)
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
		if revisions[slot], err = mintRevision(); err != nil {
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
			if err := readInto(tx, collection, entry); err != nil {
				return err
			}
		}
		for slot, entry := range pair {
			if err := setInto(tx, collection, entry, revisions[slot]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return s.writeFailed(first.Key, err)
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
		return s.writeFailed(key, err)
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
				return nil, s.unbootstrapped(in.Tier)
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
		entry, err := entryOf(in.Key(split(path)...), snapshot)
		if err != nil {
			return nil, err
		}
		found = append(found, entry)
	}
}

func writeInto(tx *firestore.Transaction, collection *firestore.CollectionRef, entry keyvalue.Entry, next keyvalue.Revision) error {
	if err := readInto(tx, collection, entry); err != nil {
		return err
	}
	return setInto(tx, collection, entry, next)
}

func readInto(tx *firestore.Transaction, collection *firestore.CollectionRef, entry keyvalue.Entry) error {
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

func setInto(tx *firestore.Transaction, collection *firestore.CollectionRef, entry keyvalue.Entry, next keyvalue.Revision) error {
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

func (s KeyValues) writeFailed(key keyvalue.Key, err error) error {
	if errors.Is(err, keyvalue.ErrStale) || errors.Is(err, keyvalue.ErrNotFound) {
		return err
	}
	if status.Code(err) == codes.NotFound {
		return s.unbootstrapped(key.Partition.Tier)
	}
	return fmt.Errorf("write %s: %w", key, err)
}

func (s KeyValues) unbootstrapped(tier environment.Tier) error {
	return refusal.Refuse(refusal.CodeNotReady,
		"this project keeps no %q Firestore database, so there is nowhere to store a record.\nRun `%s` to create it, then try again",
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
	return join(append([]string{in.Root}, in.Path...)) + keySeparator
}

func partitionCeiling(in keyvalue.Partition) string {
	return join(append([]string{in.Root}, in.Path...)) + keyCeiling
}

func documentID(key keyvalue.Key) string {
	return partitionPrefix(key.Partition) + join(key.Path)
}

func olderLayoutID(key keyvalue.Key) string {
	segments := append(append([]string{key.Partition.Root}, key.Partition.Path...), key.Path...)
	escaped := make([]string, 0, len(segments))
	for _, segment := range segments {
		escaped = append(escaped, olderSegmentEscapes.Replace(segment))
	}
	return strings.Join(escaped, segmentSeparator)
}

var olderSegmentEscapes = strings.NewReplacer("%", "%25", segmentSeparator, "%23", "/", "%2F")

func join(segments []string) string {
	escaped := make([]string, 0, len(segments))
	for _, segment := range segments {
		escaped = append(escaped, escapeSegment(segment))
	}
	return strings.Join(escaped, segmentSeparator)
}

func split(joined string) []string {
	segments := strings.Split(joined, segmentSeparator)
	path := make([]string, 0, len(segments))
	for _, segment := range segments {
		path = append(path, unescapeSegment(segment))
	}
	return path
}

var (
	segmentEscapes   = strings.NewReplacer("%", "%25", segmentSeparator, "%23", "/", "%2F", keySeparator, "%7C")
	segmentUnescapes = strings.NewReplacer("%25", "%", "%23", segmentSeparator, "%2F", "/", "%7C", keySeparator)
)

func escapeSegment(segment string) string { return segmentEscapes.Replace(segment) }

func unescapeSegment(segment string) string { return segmentUnescapes.Replace(segment) }

func mintRevision() (keyvalue.Revision, error) {
	token := make([]byte, revisionTokenSize)
	if _, err := rand.Read(token); err != nil {
		return "", fmt.Errorf("mint a revision token: %w", err)
	}
	return keyvalue.Revision(hex.EncodeToString(token)), nil
}

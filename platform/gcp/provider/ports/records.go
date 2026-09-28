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
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const (
	recordCollection  = "records-"
	segmentSeparator  = "#"
	segmentCeiling    = "$"
	bodyField         = "body"
	revisionField     = "rev"
	revisionTokenSize = 16
)

type Records struct {
	Clients *Clients
}

func (r Records) collection(name records.Name) (*firestore.CollectionRef, environment.Tier, error) {
	tier, named := stackrecords.TierOf(name)
	if !named {
		return nil, "", Tierless(name)
	}
	client, err := r.Clients.Firestore()
	if err != nil {
		return nil, tier, err
	}
	return TierRecords(client, tier), tier, nil
}

func TierRecords(client *firestore.Client, tier environment.Tier) *firestore.CollectionRef {
	return client.Collection(recordCollection + string(tier))
}

func (r Records) absent(ctx context.Context, collection *firestore.CollectionRef, tier environment.Tier) error {
	documents := collection.Select().Limit(1).Documents(ctx)
	defer documents.Stop()
	if _, err := documents.Next(); status.Code(err) == codes.NotFound {
		return r.unbootstrapped(tier)
	}
	return records.ErrNotFound
}

func (r Records) Read(ctx context.Context, name records.Name) (records.Record, error) {
	collection, tier, err := r.collection(name)
	if err != nil {
		return records.Record{}, err
	}
	snapshot, err := collection.Doc(documentID(name)).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return records.Record{}, r.absent(ctx, collection, tier)
	}
	if err != nil {
		return records.Record{}, fmt.Errorf("read %s: %w", name, err)
	}
	return recordOf(name, snapshot), nil
}

func (r Records) Write(ctx context.Context, record records.Record) (records.Revision, error) {
	collection, tier, err := r.collection(record.Name)
	if err != nil {
		return "", err
	}
	next, err := mintRevision()
	if err != nil {
		return "", err
	}
	client, err := r.Clients.Firestore()
	if err != nil {
		return "", err
	}
	err = client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		return writeInto(tx, collection, record, next)
	})
	if err != nil {
		return "", r.writeFailed(record.Name, tier, err)
	}
	return next, nil
}

func (r Records) WritePair(ctx context.Context, first, second records.Record) error {
	collection, tier, err := r.collection(first.Name)
	if err != nil {
		return err
	}
	beside, _, err := r.collection(second.Name)
	if err != nil {
		return err
	}
	if beside.Path != collection.Path {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s and %s are kept in different collections, and one transaction cannot span both", first.Name, second.Name)
	}
	revisions := make([]records.Revision, 2)
	for slot := range revisions {
		if revisions[slot], err = mintRevision(); err != nil {
			return err
		}
	}
	client, err := r.Clients.Firestore()
	if err != nil {
		return err
	}
	err = client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		pair := []records.Record{first, second}
		for _, record := range pair {
			if err := readInto(tx, collection, record); err != nil {
				return err
			}
		}
		for slot, record := range pair {
			if err := setInto(tx, collection, record, revisions[slot]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return r.writeFailed(first.Name, tier, err)
	}
	return nil
}

func (r Records) Remove(ctx context.Context, name records.Name, expected records.Revision) error {
	collection, tier, err := r.collection(name)
	if err != nil {
		return err
	}
	client, err := r.Clients.Firestore()
	if err != nil {
		return err
	}
	err = client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		reference := collection.Doc(documentID(name))
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
		return r.writeFailed(name, tier, err)
	}
	return nil
}

func (r Records) List(ctx context.Context, under records.Name) ([]records.Record, error) {
	collection, tier, err := r.collection(under)
	if err != nil {
		return nil, err
	}
	at := documentID(under)
	beneath := at + segmentSeparator
	ceiling := at + segmentCeiling

	var found []records.Record
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
				return nil, r.unbootstrapped(tier)
			}
			return nil, fmt.Errorf("read everything under %s: %w", under, err)
		}
		if id := snapshot.Ref.ID; id == at || strings.HasPrefix(id, beneath) {
			found = append(found, recordOf(nameOf(id), snapshot))
		}
	}
}

func writeInto(tx *firestore.Transaction, collection *firestore.CollectionRef, record records.Record, next records.Revision) error {
	if err := readInto(tx, collection, record); err != nil {
		return err
	}
	return setInto(tx, collection, record, next)
}

func readInto(tx *firestore.Transaction, collection *firestore.CollectionRef, record records.Record) error {
	current, exists, err := currentRevision(tx, collection.Doc(documentID(record.Name)))
	if err != nil {
		return err
	}
	if record.Revision == "" {
		if exists {
			return records.ErrStale
		}
		return nil
	}
	if !exists || current != record.Revision {
		return records.ErrStale
	}
	return nil
}

func setInto(tx *firestore.Transaction, collection *firestore.CollectionRef, record records.Record, next records.Revision) error {
	return tx.Set(collection.Doc(documentID(record.Name)), map[string]any{
		bodyField:     record.Bytes,
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

func (r Records) writeFailed(name records.Name, tier environment.Tier, err error) error {
	if errors.Is(err, records.ErrStale) || errors.Is(err, records.ErrNotFound) {
		return err
	}
	if status.Code(err) == codes.NotFound {
		return r.unbootstrapped(tier)
	}
	return fmt.Errorf("write %s: %w", name, err)
}

func (r Records) unbootstrapped(tier environment.Tier) error {
	return refusal.Refuse(refusal.CodeNotReady,
		"this project keeps no %q Firestore database, so there is nowhere to store a record.\nRun `%s` to create it, then try again",
		r.Clients.Database(), provider.BootstrapCommand(tier))
}

func recordOf(name records.Name, snapshot *firestore.DocumentSnapshot) records.Record {
	record := records.Record{Name: name, Revision: revisionOf(snapshot)}
	if body, present := snapshot.Data()[bodyField].([]byte); present {
		record.Bytes = body
	}
	return record
}

func revisionOf(snapshot *firestore.DocumentSnapshot) records.Revision {
	revision, _ := snapshot.Data()[revisionField].(string)
	return records.Revision(revision)
}

func documentID(name records.Name) string {
	escaped := make([]string, 0, len(name))
	for _, segment := range name {
		escaped = append(escaped, escapeSegment(segment))
	}
	return strings.Join(escaped, segmentSeparator)
}

func nameOf(id string) records.Name {
	segments := strings.Split(id, segmentSeparator)
	name := make(records.Name, 0, len(segments))
	for _, segment := range segments {
		name = append(name, unescapeSegment(segment))
	}
	return name
}

func escapeSegment(segment string) string {
	segment = strings.ReplaceAll(segment, "%", "%25")
	segment = strings.ReplaceAll(segment, segmentSeparator, "%23")
	return strings.ReplaceAll(segment, "/", "%2F")
}

func unescapeSegment(segment string) string {
	segment = strings.ReplaceAll(segment, "%2F", "/")
	segment = strings.ReplaceAll(segment, "%23", segmentSeparator)
	return strings.ReplaceAll(segment, "%25", "%")
}

func mintRevision() (records.Revision, error) {
	token := make([]byte, revisionTokenSize)
	if _, err := rand.Read(token); err != nil {
		return "", fmt.Errorf("mint a revision token: %w", err)
	}
	return records.Revision(hex.EncodeToString(token)), nil
}

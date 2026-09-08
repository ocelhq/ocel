package gcp

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

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	recordDatabase    = "ocel"
	recordCollection  = "records-"
	segmentSeparator  = "#"
	segmentCeiling    = "$"
	bodyField         = "body"
	revisionField     = "rev"
	revisionTokenSize = 16
)

type records struct {
	clients *clients
}

func (r records) collection(name providerkit.RecordName) (*firestore.CollectionRef, providerkit.Class, error) {
	class, named := providerkit.ClassOf(name)
	if !named {
		return nil, "", classless(name)
	}
	client, err := r.clients.Firestore()
	if err != nil {
		return nil, class, err
	}
	return client.Collection(recordCollection + string(class)), class, nil
}

func (r records) absent(ctx context.Context, collection *firestore.CollectionRef, class providerkit.Class) error {
	documents := collection.Select().Limit(1).Documents(ctx)
	defer documents.Stop()
	if _, err := documents.Next(); status.Code(err) == codes.NotFound {
		return unbootstrapped(class)
	}
	return providerkit.ErrNoRecord
}

func (r records) Read(ctx context.Context, name providerkit.RecordName) (providerkit.Record, error) {
	collection, class, err := r.collection(name)
	if err != nil {
		return providerkit.Record{}, err
	}
	snapshot, err := collection.Doc(documentID(name)).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return providerkit.Record{}, r.absent(ctx, collection, class)
	}
	if err != nil {
		return providerkit.Record{}, fmt.Errorf("read %s: %w", name, err)
	}
	return recordOf(name, snapshot), nil
}

func (r records) Write(ctx context.Context, record providerkit.Record) (providerkit.Revision, error) {
	collection, class, err := r.collection(record.Name)
	if err != nil {
		return "", err
	}
	next, err := mintRevision()
	if err != nil {
		return "", err
	}
	client, err := r.clients.Firestore()
	if err != nil {
		return "", err
	}
	err = client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		return writeInto(tx, collection, record, next)
	})
	if err != nil {
		return "", writeFailed(record.Name, class, err)
	}
	return next, nil
}

func (r records) WritePair(ctx context.Context, first, second providerkit.Record) error {
	collection, class, err := r.collection(first.Name)
	if err != nil {
		return err
	}
	beside, _, err := r.collection(second.Name)
	if err != nil {
		return err
	}
	if beside.Path != collection.Path {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"%s and %s are kept in different collections, and one transaction cannot span both", first.Name, second.Name)
	}
	revisions := make([]providerkit.Revision, 2)
	for slot := range revisions {
		if revisions[slot], err = mintRevision(); err != nil {
			return err
		}
	}
	client, err := r.clients.Firestore()
	if err != nil {
		return err
	}
	err = client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		pair := []providerkit.Record{first, second}
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
		return writeFailed(first.Name, class, err)
	}
	return nil
}

func (r records) Remove(ctx context.Context, name providerkit.RecordName, expected providerkit.Revision) error {
	collection, class, err := r.collection(name)
	if err != nil {
		return err
	}
	client, err := r.clients.Firestore()
	if err != nil {
		return err
	}
	err = client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		reference := collection.Doc(documentID(name))
		held, standing, err := revisionHeld(tx, reference)
		if err != nil {
			return err
		}
		if !standing {
			return providerkit.ErrNoRecord
		}
		if held != expected {
			return providerkit.ErrStale
		}
		return tx.Delete(reference)
	})
	if err != nil {
		return writeFailed(name, class, err)
	}
	return nil
}

func (r records) List(ctx context.Context, under providerkit.RecordName) ([]providerkit.Record, error) {
	collection, class, err := r.collection(under)
	if err != nil {
		return nil, err
	}
	at := documentID(under)
	beneath := at + segmentSeparator
	ceiling := at + segmentCeiling

	var held []providerkit.Record
	documents := collection.
		OrderBy(firestore.DocumentID, firestore.Asc).
		StartAt(at).
		EndBefore(ceiling).
		Documents(ctx)
	defer documents.Stop()
	for {
		snapshot, err := documents.Next()
		if errors.Is(err, iterator.Done) {
			return held, nil
		}
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return nil, unbootstrapped(class)
			}
			return nil, fmt.Errorf("read everything under %s: %w", under, err)
		}
		if id := snapshot.Ref.ID; id == at || strings.HasPrefix(id, beneath) {
			held = append(held, recordOf(nameOf(id), snapshot))
		}
	}
}

func writeInto(tx *firestore.Transaction, collection *firestore.CollectionRef, record providerkit.Record, next providerkit.Revision) error {
	if err := readInto(tx, collection, record); err != nil {
		return err
	}
	return setInto(tx, collection, record, next)
}

func readInto(tx *firestore.Transaction, collection *firestore.CollectionRef, record providerkit.Record) error {
	held, standing, err := revisionHeld(tx, collection.Doc(documentID(record.Name)))
	if err != nil {
		return err
	}
	if record.Revision == "" {
		if standing {
			return providerkit.ErrStale
		}
		return nil
	}
	if !standing || held != record.Revision {
		return providerkit.ErrStale
	}
	return nil
}

func setInto(tx *firestore.Transaction, collection *firestore.CollectionRef, record providerkit.Record, next providerkit.Revision) error {
	return tx.Set(collection.Doc(documentID(record.Name)), map[string]any{
		bodyField:     record.Bytes,
		revisionField: string(next),
	})
}

func revisionHeld(tx *firestore.Transaction, reference *firestore.DocumentRef) (providerkit.Revision, bool, error) {
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

func writeFailed(name providerkit.RecordName, class providerkit.Class, err error) error {
	if errors.Is(err, providerkit.ErrStale) || errors.Is(err, providerkit.ErrNoRecord) {
		return err
	}
	if status.Code(err) == codes.NotFound {
		return unbootstrapped(class)
	}
	return fmt.Errorf("write %s: %w", name, err)
}

func unbootstrapped(class providerkit.Class) error {
	return providerkit.Refuse(providerkit.CodeNotReady,
		"this project keeps no %q Firestore database, so there is nowhere to hold a record.\nRun `%s` to create it, then try again",
		recordDatabase, providerkit.BootstrapCommand(class))
}

func recordOf(name providerkit.RecordName, snapshot *firestore.DocumentSnapshot) providerkit.Record {
	record := providerkit.Record{Name: name, Revision: revisionOf(snapshot)}
	if body, held := snapshot.Data()[bodyField].([]byte); held {
		record.Bytes = body
	}
	return record
}

func revisionOf(snapshot *firestore.DocumentSnapshot) providerkit.Revision {
	revision, _ := snapshot.Data()[revisionField].(string)
	return providerkit.Revision(revision)
}

func documentID(name providerkit.RecordName) string {
	escaped := make([]string, 0, len(name))
	for _, segment := range name {
		escaped = append(escaped, escapeSegment(segment))
	}
	return strings.Join(escaped, segmentSeparator)
}

func nameOf(id string) providerkit.RecordName {
	segments := strings.Split(id, segmentSeparator)
	name := make(providerkit.RecordName, 0, len(segments))
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

func mintRevision() (providerkit.Revision, error) {
	token := make([]byte, revisionTokenSize)
	if _, err := rand.Read(token); err != nil {
		return "", fmt.Errorf("mint a revision token: %w", err)
	}
	return providerkit.Revision(hex.EncodeToString(token)), nil
}

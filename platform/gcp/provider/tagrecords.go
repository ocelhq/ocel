package gcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/cloudresourcemanager/v1"
	firestoreadmin "google.golang.org/api/firestore/v1"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const (
	tagCollection     = "tags"
	tagPrefixField    = "prefix"
	tagWrittenAtField = "writtenAt"
	tagField          = "tag"
	tagStaleField     = "stale"
	tagExpiredField   = "expired"

	tagRecordsRole = "roles/datastore.user"

	reasonTagsUnindexed = "it has no index the tag records of an app are read through"
)

func tagIndexes() []*firestoreadmin.GoogleFirestoreAdminV1Index {
	return []*firestoreadmin.GoogleFirestoreAdminV1Index{{
		QueryScope: collectionScoped,
		Fields: []*firestoreadmin.GoogleFirestoreAdminV1IndexField{
			{FieldPath: tagPrefixField, Order: ascending},
			{FieldPath: tagWrittenAtField, Order: ascending},
		},
	}}
}

func unindexedTagFields() []string {
	return []string{tagWrittenAtField, tagField, tagStaleField, tagExpiredField}
}

func tagDatabaseCondition(c *clients, tier environment.Tier) *cloudresourcemanager.Expr {
	return &cloudresourcemanager.Expr{
		Title:      "ocel " + string(c.Namespace()) + " " + string(tier) + " tag records",
		Expression: fmt.Sprintf("resource.name == %q", databasePath(c, c.TagDatabase(tier))),
	}
}

func (b bootstrap) ensureTagIndexes(ctx context.Context, tier environment.Tier) error {
	service, err := b.clients.Databases()
	if err != nil {
		return err
	}
	database := databasePath(b.clients, b.clients.TagDatabase(tier))
	for _, field := range unindexedTagFields() {
		name := database + "/collectionGroups/" + tagCollection + "/fields/" + field
		operation, err := attempted(ctx, service.Projects.Databases.CollectionGroups.Fields.Patch(name,
			&firestoreadmin.GoogleFirestoreAdminV1Field{IndexConfig: &firestoreadmin.GoogleFirestoreAdminV1IndexConfig{
				Indexes:         []*firestoreadmin.GoogleFirestoreAdminV1Index{},
				ForceSendFields: []string{"Indexes"},
			}}).UpdateMask("indexConfig").Context(ctx).Do)
		if err != nil {
			return fmt.Errorf("leave %s of the tag records unindexed: %w", field, err)
		}
		if err := b.awaited(ctx, "leaving "+field+" of the tag records unindexed", operation); err != nil {
			return err
		}
	}
	parent := database + "/collectionGroups/" + tagCollection
	for _, index := range tagIndexes() {
		operation, err := attempted(ctx, service.Projects.Databases.CollectionGroups.Indexes.Create(parent, index).Context(ctx).Do)
		if taken(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("index the tag records of %s: %w", b.clients.TagDatabase(tier), err)
		}
		if err := b.awaited(ctx, "building the index on the tag records of "+b.clients.TagDatabase(tier), operation); err != nil {
			return err
		}
	}
	return nil
}

func (b bootstrap) tagDatabasePresence(ctx context.Context, id string) (presence, error) {
	found, err := b.databasePresence(ctx, id)
	if err != nil || !found.present || b.clients.emulated() {
		return found, err
	}
	if found.mends != "" {
		return found, nil
	}
	service, err := b.clients.Databases()
	if err != nil {
		return presence{}, err
	}
	listed, err := attempted(ctx, service.Projects.Databases.CollectionGroups.Indexes.List(
		databasePath(b.clients, id)+"/collectionGroups/"+tagCollection).Context(ctx).Do)
	if err != nil {
		return presence{}, fmt.Errorf("read the indexes of the tag records in %q: %w", id, err)
	}
	for _, index := range listed.Indexes {
		if index.State == "READY" && readsTagsByPrefix(index) {
			return found, nil
		}
	}
	found.mends = reasonTagsUnindexed
	return found, nil
}

func readsTagsByPrefix(index *firestoreadmin.GoogleFirestoreAdminV1Index) bool {
	want := tagIndexes()[0]
	if index.QueryScope != want.QueryScope || len(index.Fields) < len(want.Fields) {
		return false
	}
	for i, field := range want.Fields {
		if index.Fields[i].FieldPath != field.FieldPath || index.Fields[i].Order != field.Order {
			return false
		}
	}
	return true
}

func (a artifacts) removeTagRecords(ctx context.Context, tier environment.Tier, prefix string, log progress.Log) error {
	resolved, err := a.p.openClients(ctx)
	if err != nil {
		return err
	}
	store, err := resolved.Workload().TagFirestore(tier)
	if err != nil {
		return err
	}
	start := strings.TrimSuffix(prefix, "/") + "/"
	records := store.Collection(tagCollection).
		Where(tagPrefixField, ">=", start).
		Where(tagPrefixField, "<", start+"\uf8ff").
		Select().Documents(ctx)
	defer records.Stop()

	writer := store.BulkWriter(ctx)
	var jobs []*firestore.BulkWriterJob
	var errs []error
	for {
		record, err := records.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			if status.Code(err) == codes.NotFound && len(jobs) == 0 {
				writer.End()
				return nil
			}
			errs = append(errs, fmt.Errorf("list the tag records under %s: %w", prefix, err))
			break
		}
		job, err := writer.Delete(record.Ref)
		if err != nil {
			errs = append(errs, fmt.Errorf("delete %s: %w", record.Ref.Path, err))
			break
		}
		jobs = append(jobs, job)
	}
	writer.End()
	removed := 0
	for _, job := range jobs {
		if _, err := job.Results(); err != nil {
			errs = append(errs, fmt.Errorf("delete a tag record under %s: %w", prefix, err))
			continue
		}
		removed++
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	if removed > 0 {
		ensureProgress(log).Say(fmt.Sprintf("Removed %d tag records under %s from Firestore database %s",
			removed, prefix, ports.TagDatabase(resolved.Namespace(), tier)))
	}
	return nil
}

package gcp

import (
	"context"
	"errors"
	"fmt"

	"cloud.google.com/go/firestore"
	firestoreadmin "google.golang.org/api/firestore/v1"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/task"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/variables"
)

const (
	tagCollection     = "tags"
	tagPrefixField    = "prefix"
	tagWrittenAtField = "writtenAt"
	tagField          = "tag"
	tagStaleField     = "stale"
	tagExpiredField   = "expired"
)

func tagIndexes() []*firestoreadmin.GoogleFirestoreAdminV1Index {
	return []*firestoreadmin.GoogleFirestoreAdminV1Index{{
		QueryScope: task.CollectionScoped,
		Fields: []*firestoreadmin.GoogleFirestoreAdminV1IndexField{
			{FieldPath: tagPrefixField, Order: task.Ascending},
			{FieldPath: tagWrittenAtField, Order: task.Ascending},
		},
	}}
}

func unindexedTagFields() []string {
	return []string{tagWrittenAtField, tagField, tagStaleField, tagExpiredField}
}

func tagLayout() variables.Layout {
	layout := variables.Layout{}
	for _, field := range unindexedTagFields() {
		layout.Unindexed = append(layout.Unindexed, variables.LayoutField{Group: tagCollection, Path: field})
	}
	for _, index := range tagIndexes() {
		layout.Indexes = append(layout.Indexes, variables.LayoutIndex{Group: tagCollection, Index: index})
	}
	return layout
}

func (a artifacts) removeTagRecords(ctx context.Context, tier environment.Tier, scope naming.StorageScope, log progress.Log) error {
	prefix, err := scope.Prefix(naming.ObjectPages)
	if err != nil {
		return err
	}
	resolved, err := a.p.openClients(ctx)
	if err != nil {
		return err
	}
	database, err := resolved.Workload().ReadProjectDatabase(ctx, tier, scope.Project())
	if err != nil {
		return err
	}
	store, err := resolved.Workload().TagRecordsFirestore(ctx, tier, scope.Project())
	if err != nil {
		return err
	}
	records := googleretry.QueryDocuments(ctx, store.Collection(tagCollection).
		Where(tagPrefixField, ">=", prefix).
		Where(tagPrefixField, "<", prefix+"\uf8ff").
		Select())
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
		gcloud.EnsureProgress(log).Say(fmt.Sprintf("Removed %d tag records under %s from Firestore database %s",
			removed, prefix, database))
	}
	return nil
}

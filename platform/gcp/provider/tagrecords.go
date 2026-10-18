package gcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"cloud.google.com/go/firestore"
	firestoreadmin "google.golang.org/api/firestore/v1"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/task"
)

const (
	tagCollection     = "tags"
	tagPrefixField    = "prefix"
	tagWrittenAtField = "writtenAt"
	tagField          = "tag"
	tagStaleField     = "stale"
	tagExpiredField   = "expired"

	tagRecordsRole = "roles/datastore.user"

	tagDatabasePart    = "tag-database"
	tagDatabaseSummary = "the tag records of every Next app this tier serves: one document per revalidated tag, read by every instance of the app"
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

func tagDatabaseCondition(c *gcloud.Clients, tier environment.Tier) *policy.Condition {
	return &policy.Condition{
		Title:      "ocel " + string(c.Namespace()) + " " + string(tier) + " tag records",
		Expression: fmt.Sprintf("resource.name == %q", bootstrap.DatabasePath(c, c.TagDatabase(tier))),
	}
}

func newTagDatabasePart(b bootstrap.Engine, tier environment.Tier) bootstrap.Part {
	var resources []provider.PartResource
	if !b.Clients.Emulated() {
		resources = append(resources, provider.PartResource{Kind: string(bootstrap.KindDatabase), ID: b.Clients.TagDatabase(tier)})
	}
	return bootstrap.Part{
		Name:      tagDatabasePart,
		Summary:   tagDatabaseSummary,
		Resources: resources,
		Shape:     tagDatabaseShape(b.Clients.Names, tier, b.Clients.Region),
		Make:      func(ctx context.Context) error { return makeTagDatabase(ctx, b, tier) },
		Users:     func(ctx context.Context) ([]string, error) { return listTagDatabaseUsers(ctx, b, tier) },
		Remove:    func(ctx context.Context) error { return removeTagDatabase(ctx, b, tier) },
	}
}

func listTagDatabaseUsers(ctx context.Context, b bootstrap.Engine, tier environment.Tier) ([]string, error) {
	users, err := stackrecords.ListPartUsers(ctx, b.Records, tier)
	if err != nil {
		return nil, err
	}
	return append([]string{}, users[tagDatabasePart]...), nil
}

func tagDatabaseShape(names names.Names, tier environment.Tier, region string) []pricing.Shaped {
	return []pricing.Shaped{{Name: names.TagDatabase(tier), Type: cost.FirestoreDatabaseType, Properties: map[string]any{"location_id": region, "type": bootstrap.NativeFirestore}}}
}

func makeTagDatabase(ctx context.Context, b bootstrap.Engine, tier environment.Tier) error {
	if b.Clients.Emulated() {
		return nil
	}
	if err := b.MakeDatabase(ctx, bootstrap.Survey{Project: b.Clients.Project(), Region: b.Clients.Region}, b.Clients.TagDatabase(tier)); err != nil {
		return err
	}
	return ensureTagIndexes(ctx, b, tier)
}

func removeTagDatabase(ctx context.Context, b bootstrap.Engine, tier environment.Tier) error {
	if b.Clients.Emulated() {
		return nil
	}
	return b.TakeDatabase(ctx, b.Clients.TagDatabase(tier))
}

func ensureTagIndexes(ctx context.Context, b bootstrap.Engine, tier environment.Tier) error {
	service, err := b.Clients.Databases()
	if err != nil {
		return err
	}
	database := bootstrap.DatabasePath(b.Clients, b.Clients.TagDatabase(tier))
	for _, field := range unindexedTagFields() {
		name := database + "/collectionGroups/" + tagCollection + "/fields/" + field
		operation, err := googleretry.Attempted(ctx, service.Projects.Databases.CollectionGroups.Fields.Patch(name,
			&firestoreadmin.GoogleFirestoreAdminV1Field{IndexConfig: &firestoreadmin.GoogleFirestoreAdminV1IndexConfig{
				Indexes:         []*firestoreadmin.GoogleFirestoreAdminV1Index{},
				ForceSendFields: []string{"Indexes"},
			}}).UpdateMask("indexConfig").Context(ctx).Do)
		if err != nil {
			return fmt.Errorf("leave %s of the tag records unindexed: %w", field, err)
		}
		if err := b.Awaited(ctx, "leaving "+field+" of the tag records unindexed", operation); err != nil {
			return err
		}
	}
	parent := database + "/collectionGroups/" + tagCollection
	for _, index := range tagIndexes() {
		operation, err := googleretry.Attempted(ctx, service.Projects.Databases.CollectionGroups.Indexes.Create(parent, index).Context(ctx).Do)
		if gcloud.Taken(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("index the tag records of %s: %w", b.Clients.TagDatabase(tier), err)
		}
		if err := b.Awaited(ctx, "building the index on the tag records of "+b.Clients.TagDatabase(tier), operation); err != nil {
			return err
		}
	}
	return nil
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
		gcloud.EnsureProgress(log).Say(fmt.Sprintf("Removed %d tag records under %s from Firestore database %s",
			removed, prefix, ports.TagDatabase(resolved.Namespace(), tier)))
	}
	return nil
}

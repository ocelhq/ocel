package topics

import (
	"context"
	"errors"
	"fmt"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

func RemoveProjectRuns(ctx context.Context, clients *ports.Clients, tier environment.Tier, slug string) error {
	client, err := clients.ProjectFirestore(ctx, tier, slug)
	if err != nil {
		return err
	}
	environments, err := projectDocument(client, tier, slug).Collection(environmentsCollection).DocumentRefs(ctx).GetAll()
	if err != nil {
		return fmt.Errorf("list the environments %s keeps runs of in %s: %w", slug, tier, err)
	}
	writer := client.BulkWriter(ctx)
	var jobs []*firestore.BulkWriterJob
	var errs []error
	for _, scope := range environments {
		for _, group := range []string{runsCollection, recordsCollection} {
			queued, err := queueDeletes(ctx, writer, scope.Collection(group))
			jobs = append(jobs, queued...)
			if err != nil {
				errs = append(errs, err)
			}
		}
	}
	writer.End()
	for _, job := range jobs {
		if _, err := job.Results(); err != nil {
			errs = append(errs, fmt.Errorf("delete a run of %s in %s: %w", slug, tier, err))
		}
	}
	return errors.Join(errs...)
}

func queueDeletes(ctx context.Context, writer *firestore.BulkWriter, collection *firestore.CollectionRef) ([]*firestore.BulkWriterJob, error) {
	documents := collection.Select().Documents(ctx)
	defer documents.Stop()
	var jobs []*firestore.BulkWriterJob
	for {
		document, err := documents.Next()
		if errors.Is(err, iterator.Done) {
			return jobs, nil
		}
		if err != nil {
			return jobs, fmt.Errorf("list %s: %w", collection.Path, err)
		}
		job, err := writer.Delete(document.Ref)
		if err != nil {
			return jobs, fmt.Errorf("delete %s: %w", document.Ref.Path, err)
		}
		jobs = append(jobs, job)
	}
}

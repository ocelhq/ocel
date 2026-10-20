package variables

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	firestoreadmin "google.golang.org/api/firestore/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
)

const (
	writableDatabasesRecord = "writabledatabases"
	reasonWritableFirstUse  = "added on first use: the Firestore database project %s's runs and tag records are kept in, which its apps alone may write"
)

type LayoutField struct {
	Group string
	Path  string
}

type LayoutIndex struct {
	Group string
	Index *firestoreadmin.GoogleFirestoreAdminV1Index
}

type Layout struct {
	Expiring  []LayoutField
	Unindexed []LayoutField
	Indexes   []LayoutIndex
}

type writableDatabaseRecord struct {
	Database string `json:"database"`
}

func writableDatabaseKey(tier environment.Tier, project string) records.Key {
	return records.Partition{Tier: tier, Root: stackrecords.RootBootstrap}.Key(writableDatabasesRecord, project)
}

func ProjectWritableDatabaseCondition(c *gcloud.Clients, tier environment.Tier, project string) *policy.Condition {
	return &policy.Condition{
		Title:      "ocel " + project + " " + string(tier) + " runs and tag records",
		Expression: fmt.Sprintf("resource.name == %q", bootstrap.DatabasePath(c, c.ProjectWritableDatabase(tier, project))),
	}
}

func PlanProjectWritableDatabase(ctx context.Context, b bootstrap.Engine, pre provider.DeployPreflight) (*provider.ChangeGroup, error) {
	tier, project := pre.Deploy.Tier, pre.Deploy.Slug
	recorded, err := records.ReadOrEmpty(ctx, b.Records, writableDatabaseKey(tier, project))
	if err != nil || len(recorded.Value) > 0 {
		return nil, err
	}
	database := b.Clients.ProjectWritableDatabase(tier, project)
	return &provider.ChangeGroup{
		Kind:    provider.StackGroupKind,
		Name:    string(gcloud.Vendor) + "/" + b.Clients.Project() + "/" + string(tier) + "/" + database,
		Action:  provider.ActionCreate,
		Reason:  fmt.Sprintf(reasonWritableFirstUse, project),
		Changes: []provider.Change{{Kind: string(bootstrap.KindDatabase), Name: database, Action: provider.ActionCreate}},
	}, nil
}

func EnsureProjectWritableDatabase(ctx context.Context, b bootstrap.Engine, pre provider.DeployPreflight, layouts ...Layout) error {
	tier, project := pre.Deploy.Tier, pre.Deploy.Slug
	recorded, err := records.ReadOrEmpty(ctx, b.Records, writableDatabaseKey(tier, project))
	if err != nil || len(recorded.Value) > 0 {
		return err
	}
	database := b.Clients.ProjectWritableDatabase(tier, project)
	if !b.Clients.Emulated() {
		if err := makeWritableDatabase(ctx, b, tier, database); err != nil {
			return err
		}
		for _, layout := range layouts {
			if err := makeLayout(ctx, b, database, layout); err != nil {
				return gcloud.ExplainMissingGrant(err, b.Clients.DeployCredential(tier).Tiered(credential.DatabasesRole).String())
			}
		}
	}
	value, err := json.Marshal(writableDatabaseRecord{Database: database})
	if err != nil {
		return err
	}
	return records.Change(ctx, b.Records, writableDatabaseKey(tier, project), func(recorded records.Entry) ([]byte, bool, error) {
		return value, string(recorded.Value) != string(value), nil
	})
}

func makeWritableDatabase(ctx context.Context, b bootstrap.Engine, tier environment.Tier, database string) error {
	err := b.MakeTierDatabase(ctx, tier, database)
	if googleretry.AnsweredCode(err) == http.StatusTooManyRequests || err != nil && strings.Contains(strings.ToLower(err.Error()), databaseQuotaMessage) {
		return refusal.Refuse(refusal.CodeNotReady,
			"project %s has as many Firestore databases as Firestore allows, and every Ocel project that runs topics, tasks or a revalidating Next app keeps its runs and tag records in one of its own per tier.\n"+
				"Ask Google support to raise the limit of databases per project, or remove projects this tier no longer runs, then try again", b.Clients.Project())
	}
	return gcloud.ExplainMissingGrant(err, b.Clients.DeployCredential(tier).Tiered(credential.DatabasesRole).String())
}

func makeLayout(ctx context.Context, b bootstrap.Engine, database string, layout Layout) error {
	service, err := b.Clients.Databases()
	if err != nil {
		return err
	}
	path := bootstrap.DatabasePath(b.Clients, database)
	for _, field := range layout.Expiring {
		operation, err := googleretry.Attempted(ctx, service.Projects.Databases.CollectionGroups.Fields.Patch(fieldPath(path, field),
			&firestoreadmin.GoogleFirestoreAdminV1Field{TtlConfig: &firestoreadmin.GoogleFirestoreAdminV1TtlConfig{}}).
			UpdateMask("ttlConfig").Context(ctx).Do)
		if err != nil {
			return fmt.Errorf("let Firestore delete %s once %s passes: %w", field.Group, field.Path, err)
		}
		if err := b.Awaited(ctx, fmt.Sprintf("setting the time to live of %s on %s", field.Group, field.Path), operation); err != nil {
			return err
		}
	}
	for _, field := range layout.Unindexed {
		operation, err := googleretry.Attempted(ctx, service.Projects.Databases.CollectionGroups.Fields.Patch(fieldPath(path, field),
			&firestoreadmin.GoogleFirestoreAdminV1Field{IndexConfig: &firestoreadmin.GoogleFirestoreAdminV1IndexConfig{
				Indexes:         []*firestoreadmin.GoogleFirestoreAdminV1Index{},
				ForceSendFields: []string{"Indexes"},
			}}).UpdateMask("indexConfig").Context(ctx).Do)
		if err != nil {
			return fmt.Errorf("leave %s of %s unindexed: %w", field.Path, field.Group, err)
		}
		if err := b.Awaited(ctx, "leaving "+field.Path+" of "+field.Group+" unindexed", operation); err != nil {
			return err
		}
	}
	for _, index := range layout.Indexes {
		operation, err := googleretry.Attempted(ctx, service.Projects.Databases.CollectionGroups.Indexes.Create(path+"/collectionGroups/"+index.Group, index.Index).Context(ctx).Do)
		if gcloud.Taken(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("index the %s of %s: %w", index.Group, database, err)
		}
		if err := b.Awaited(ctx, "building an index on the "+index.Group+" of "+database, operation); err != nil {
			return err
		}
	}
	return nil
}

func fieldPath(database string, field LayoutField) string {
	return database + "/collectionGroups/" + field.Group + "/fields/" + field.Path
}

func takeWritableDatabase(ctx context.Context, b bootstrap.Engine, stores projectStores, tier environment.Tier, project string, log progress.Log) error {
	recorded, err := records.ReadOrEmpty(ctx, b.Records, writableDatabaseKey(tier, project))
	if err != nil || len(recorded.Value) == 0 {
		return err
	}
	database := b.Clients.ProjectWritableDatabase(tier, project)
	if !b.Clients.Emulated() {
		gcloud.EnsureProgress(log).Say(fmt.Sprintf("Deleting Firestore database %s, which kept the runs and tag records of %s in %s", database, project, tier))
		if err := stores.takeDatabase(ctx, tier, database); err != nil {
			return gcloud.ExplainMissingGrant(err, b.Clients.DeployCredential(tier).Tiered(credential.DatabasesRole).String())
		}
	}
	return records.Forget(ctx, b.Records, writableDatabaseKey(tier, project))
}

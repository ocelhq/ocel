package variables

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	firestoreadmin "google.golang.org/api/firestore/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const (
	SharedDatabasePartName = "app-database"
	sharedDatabaseSummary  = "the Firestore database every project of this tier keeps its values, runs and tag records in, unless it sets ownDatabase"
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

func DatabaseCondition(c *gcloud.Clients, database string) *policy.Condition {
	return &policy.Condition{
		Title:      "ocel " + database,
		Expression: fmt.Sprintf("resource.name == %q", bootstrap.DatabasePath(c, database)),
	}
}

func ProjectDatabaseCondition(ctx context.Context, c *gcloud.Clients, tier environment.Tier, project string) (*policy.Condition, error) {
	database, err := c.Workload().ReadProjectDatabase(ctx, tier, project)
	if err != nil {
		return nil, err
	}
	return DatabaseCondition(c, database), nil
}

func SharedDatabasePart(layouts ...Layout) bootstrap.RemovablePart {
	return bootstrap.RemovablePart{
		Part: provider.Part{Name: SharedDatabasePartName, Summary: sharedDatabaseSummary},
		Deployed: func(b bootstrap.Engine, tier environment.Tier) bootstrap.Part {
			return sharedDatabasePart(b, tier, layouts)
		},
	}
}

func sharedDatabasePart(b bootstrap.Engine, tier environment.Tier, layouts []Layout) bootstrap.Part {
	database := b.Clients.SharedDatabase(tier)
	content, _ := json.Marshal(layouts)
	part := bootstrap.Part{
		Name:      SharedDatabasePartName,
		Summary:   sharedDatabaseSummary,
		Content:   string(content),
		Resources: []provider.PhysicalResource{{Kind: string(bootstrap.KindDatabase), ID: database}},
		Make:      func(ctx context.Context) error { return ensureAppDatabase(ctx, b, tier, database, layouts) },
		Remove: func(ctx context.Context) error {
			if b.Clients.Emulated() {
				return nil
			}
			return gcloud.ExplainMissingGrant(b.TakeTierDatabase(ctx, tier, database), b.Clients.DeployCredential(tier).Tiered(credential.DatabasesRole).String())
		},
		Users: func(ctx context.Context) ([]string, error) { return listSharingProjects(ctx, b, tier) },
		RefuseInUse: func(ctx context.Context, removal bootstrap.PartRemoval) error {
			users, err := listSharingProjects(ctx, b, tier)
			if err != nil || len(users) == 0 {
				return err
			}
			return refusal.Refuse(refusal.CodeInvalid,
				"%s keep their values, runs and tag records in the %s part of tier %s, and taking it down loses every one of them.\n"+
					"Destroy those projects in the tier, then run `%s` again",
				strings.Join(users, ", "), SharedDatabasePartName, tier, removal.Retry)
		},
	}
	if shaped, priced := (bootstrap.Resource{Kind: bootstrap.KindDatabase, Name: database}).Shape(b.Clients.Region); priced {
		part.Shape = []pricing.Shaped{shaped}
	}
	return part
}

func listSharingProjects(ctx context.Context, b bootstrap.Engine, tier environment.Tier) ([]string, error) {
	recorded, err := ports.ListProjectDatabases(ctx, b.Records, tier)
	if err != nil {
		return nil, err
	}
	var sharing []string
	for project, database := range recorded {
		if database == b.Clients.SharedDatabase(tier) {
			sharing = append(sharing, project)
		}
	}
	slices.Sort(sharing)
	return sharing, nil
}

func PlanProjectDatabase(ctx context.Context, b bootstrap.Engine, tier environment.Tier, project string, own bool) ([]provider.ChangeGroup, error) {
	recorded, found, err := ports.ReadProjectDatabaseRecord(ctx, b.Records, tier, project)
	if err != nil {
		return nil, err
	}
	if found {
		return nil, refuseMovedDatabase(b.Clients, tier, project, recorded, own)
	}
	if !own {
		return b.PlanParts(ctx, tier, []string{SharedDatabasePartName})
	}
	database := b.Clients.OwnDatabase(tier, project)
	return []provider.ChangeGroup{{
		Kind:    provider.StackGroupKind,
		Name:    string(gcloud.Vendor) + "/" + b.Clients.Project() + "/" + string(tier) + "/" + database,
		Action:  provider.ActionCreate,
		Reason:  bootstrap.ReasonFirstUse,
		Changes: []provider.Change{{Kind: string(bootstrap.KindDatabase), Name: database, Action: provider.ActionCreate}},
	}}, nil
}

func EnsureProjectDatabase(ctx context.Context, b bootstrap.Engine, tier environment.Tier, project string, own bool, layouts ...Layout) error {
	recorded, found, err := ports.ReadProjectDatabaseRecord(ctx, b.Records, tier, project)
	if err != nil {
		return err
	}
	if found {
		return refuseMovedDatabase(b.Clients, tier, project, recorded, own)
	}
	return recordProjectDatabase(ctx, b, tier, project, own, layouts)
}

func EnsureProjectDatabaseRecorded(ctx context.Context, b bootstrap.Engine, tier environment.Tier, project string, own bool, layouts ...Layout) error {
	_, found, err := ports.ReadProjectDatabaseRecord(ctx, b.Records, tier, project)
	if err != nil || found {
		return err
	}
	return recordProjectDatabase(ctx, b, tier, project, own, layouts)
}

func recordProjectDatabase(ctx context.Context, b bootstrap.Engine, tier environment.Tier, project string, own bool, layouts []Layout) error {
	database := b.Clients.SharedDatabase(tier)
	if own {
		database = b.Clients.OwnDatabase(tier, project)
		if err := ensureAppDatabase(ctx, b, tier, database, layouts); err != nil {
			return err
		}
	} else if err := b.EnsurePart(ctx, tier, project, sharedDatabasePart(b, tier, layouts)); err != nil {
		return err
	}
	value, err := ports.ProjectDatabaseRecord(database)
	if err != nil {
		return err
	}
	return records.Change(ctx, b.Records, ports.ProjectDatabaseKey(tier, project), func(recorded records.Entry) ([]byte, bool, error) {
		if len(recorded.Value) == 0 {
			return value, true, nil
		}
		held, err := ports.DecodeProjectDatabaseRecord(recorded)
		if err != nil {
			return nil, false, err
		}
		return nil, false, refuseMovedDatabase(b.Clients, tier, project, held, own)
	})
}

func refuseMovedDatabase(c *gcloud.Clients, tier environment.Tier, project, recorded string, own bool) error {
	switch {
	case own && recorded != c.OwnDatabase(tier, project):
		return refusal.Refuse(refusal.CodeInvalid,
			"project %s keeps its %s records in Firestore database %s, which its tier's projects share, and ownDatabase moves none of them.\n"+
				"Turn ownDatabase off, or run `ocel projects destroy %s` and deploy it again",
			project, tier, recorded, project)
	case !own && recorded != c.SharedDatabase(tier):
		return refusal.Refuse(refusal.CodeInvalid,
			"project %s keeps its %s records in Firestore database %s of its own, which it reads only while ownDatabase is on.\n"+
				"Turn ownDatabase back on, or run `ocel projects destroy %s` and deploy it again",
			project, tier, recorded, project)
	}
	return nil
}

func ensureAppDatabase(ctx context.Context, b bootstrap.Engine, tier environment.Tier, database string, layouts []Layout) error {
	if b.Clients.Emulated() {
		return nil
	}
	if err := makeAppDatabase(ctx, b, tier, database, layouts); err != nil {
		return err
	}
	return ports.UntilDatabaseServes(ctx, b.Clients.Workload(), database)
}

func makeAppDatabase(ctx context.Context, b bootstrap.Engine, tier environment.Tier, database string, layouts []Layout) error {
	err := b.MakeTierDatabase(ctx, tier, database)
	if googleretry.AnsweredCode(err) == http.StatusTooManyRequests || err != nil && strings.Contains(strings.ToLower(err.Error()), databaseQuotaMessage) {
		return refusal.Refuse(refusal.CodeNotReady,
			"project %s has as many Firestore databases as Firestore allows, so ocel cannot make %s.\n"+
				"Ask Google support to raise the limit of databases per project, or turn ownDatabase off for projects that need no database of their own, then try again",
			b.Clients.Project(), database)
	}
	if err != nil {
		return gcloud.ExplainMissingGrant(err, b.Clients.DeployCredential(tier).Tiered(credential.DatabasesRole).String())
	}
	for _, layout := range layouts {
		if err := makeLayout(ctx, b, database, layout); err != nil {
			return gcloud.ExplainMissingGrant(err, b.Clients.DeployCredential(tier).Tiered(credential.DatabasesRole).String())
		}
	}
	return nil
}

func makeLayout(ctx context.Context, b bootstrap.Engine, database string, layout Layout) error {
	service, err := b.Clients.Databases()
	if err != nil {
		return err
	}
	path := bootstrap.DatabasePath(b.Clients, database)
	for _, field := range layout.Expiring {
		operation, err := googleretry.AttemptedUntilServed(ctx, service.Projects.Databases.CollectionGroups.Fields.Patch(fieldPath(path, field),
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
		operation, err := googleretry.AttemptedUntilServed(ctx, service.Projects.Databases.CollectionGroups.Fields.Patch(fieldPath(path, field),
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
		operation, err := googleretry.AttemptedUntilServed(ctx, service.Projects.Databases.CollectionGroups.Indexes.Create(path+"/collectionGroups/"+index.Group, index.Index).Context(ctx).Do)
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

func takeProjectDatabase(ctx context.Context, b bootstrap.Engine, stores projectStores, tier environment.Tier, project string, log progress.Log) error {
	recorded, found, err := ports.ReadProjectDatabaseRecord(ctx, b.Records, tier, project)
	if err != nil || !found {
		return err
	}
	if recorded == b.Clients.SharedDatabase(tier) {
		if err := stores.takeRuns(ctx, tier, project); err != nil {
			return err
		}
	} else if !b.Clients.Emulated() {
		gcloud.EnsureProgress(log).Say("Deleting Firestore database " + recorded)
		if err := stores.takeDatabase(ctx, tier, recorded); err != nil {
			return gcloud.ExplainMissingGrant(err, b.Clients.DeployCredential(tier).Tiered(credential.DatabasesRole).String())
		}
	}
	return records.Forget(ctx, b.Records, ports.ProjectDatabaseKey(tier, project))
}

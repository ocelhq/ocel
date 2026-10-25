package variables

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
)

const (
	databaseQuotaMessage   = "maximum number of databases"
	projectDatabasesFilter = "databases/"
)

func RecordsCondition(c *gcloud.Clients, tiers ...environment.Tier) *policy.Condition {
	var clauses []string
	for _, tier := range tiers {
		clauses = append(clauses,
			fmt.Sprintf("resource.name == %q", bootstrap.DatabasePath(c, c.RecordsDatabase(tier))),
			fmt.Sprintf("resource.name == %q", bootstrap.DatabasePath(c, c.SharedDatabase(tier))),
			fmt.Sprintf("resource.name.startsWith(%q)", bootstrap.DatabasePath(c, c.OwnDatabasePrefix(tier))))
	}
	title := "ocel " + string(c.Namespace()) + " records"
	if len(tiers) == 1 {
		title = "ocel " + string(c.Namespace()) + " " + string(tiers[0]) + " records"
	}
	return &policy.Condition{Title: title, Expression: strings.Join(clauses, " || ")}
}

func TakeProjectRecords(ctx context.Context, b bootstrap.Engine, tier environment.Tier) error {
	if b.Clients.Emulated() {
		return nil
	}
	service, err := b.Clients.Databases()
	if err != nil {
		return err
	}
	listed, err := googleretry.Attempted(ctx, service.Projects.Databases.List("projects/"+b.Clients.Project()).Context(ctx).Do)
	if err != nil {
		return fmt.Errorf("list the Firestore databases of project %s: %w", b.Clients.Project(), err)
	}
	for _, database := range listed.Databases {
		_, id, _ := strings.Cut(database.Name, projectDatabasesFilter)
		if !strings.HasPrefix(id, b.Clients.OwnDatabasePrefix(tier)) {
			continue
		}
		if err := b.TakeTierDatabase(ctx, tier, id); err != nil {
			return err
		}
	}
	return nil
}

func ReferencedOwners(ctx context.Context, keyValues records.Store, cipher seal.Cipher, spec provider.StackSpec) ([]string, error) {
	if len(spec.App.Values.Secrets) == 0 {
		return nil, nil
	}
	store := variablestore.Store{KeyValues: keyValues, Cipher: cipher}
	owners, err := store.ReferenceOwners(ctx, variablestore.Scope{Project: spec.Ref.Project, Tier: spec.Ref.Tier})
	if err != nil {
		return nil, err
	}
	environments := []string{""}
	if live := LiveEnvironment(spec.Ref); live != "" {
		environments = append(environments, live)
	}
	var referenced []string
	for _, secret := range spec.App.Values.Secrets {
		for _, env := range environments {
			cell := variablestore.Coordinate{Cell: variablestore.Cell{Folder: secret.Folder, Key: secret.Key}, Environment: env}
			if owner := owners[cell]; owner != "" && !slices.Contains(referenced, owner) {
				referenced = append(referenced, owner)
			}
		}
	}
	slices.Sort(referenced)
	return referenced, nil
}

func LiveEnvironment(ref provider.StackRef) string {
	if ref.Tier == environment.TierProduction {
		return ""
	}
	return ref.Name.Env
}

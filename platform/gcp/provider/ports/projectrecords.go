package ports

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"cloud.google.com/go/firestore"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

const (
	projectDatabaseInfix         = "-v-"
	projectWritableDatabaseInfix = "-w-"
	projectKeyInfix              = "-p-"
	projectHashLength            = 10
	projectField                 = "project"
)

var (
	ErrNoProjectRecords = errors.New("the project keeps no records database in this tier")
	ErrNoProjectKey     = errors.New("the project has no key in this tier")
)

func projectHash(tier environment.Tier, project string) string {
	sum := sha256.Sum256([]byte(string(tier) + "\x00" + project))
	return hex.EncodeToString(sum[:])[:projectHashLength]
}

func ProjectDatabasePrefix(namespace provider.Namespace, tier environment.Tier) string {
	return string(namespace) + "-" + string(tier) + projectDatabaseInfix
}

func ProjectDatabase(namespace provider.Namespace, tier environment.Tier, project string) string {
	return ProjectDatabasePrefix(namespace, tier) + projectHash(tier, project)
}

func ProjectWritableDatabasePrefix(namespace provider.Namespace, tier environment.Tier) string {
	return string(namespace) + "-" + string(tier) + projectWritableDatabaseInfix
}

func ProjectWritableDatabase(namespace provider.Namespace, tier environment.Tier, project string) string {
	return ProjectWritableDatabasePrefix(namespace, tier) + projectHash(tier, project)
}

func ProjectKeyPrefix(tier environment.Tier) string { return string(tier) + projectKeyInfix }

func ProjectKey(tier environment.Tier, project string) string {
	return ProjectKeyPrefix(tier) + projectHash(tier, project)
}

func (c *Clients) ProjectDatabase(tier environment.Tier, project string) string {
	return ProjectDatabase(c.Namespace, tier, project)
}

func (c *Clients) ProjectKeyPath(tier environment.Tier, project string) string {
	return c.KeyPath(ProjectKey(tier, project))
}

func (c *Clients) ProjectFirestore(tier environment.Tier, project string) (*firestore.Client, error) {
	database := c.ProjectDatabase(tier, project)
	held, _ := c.projectStores.LoadOrStore(database, &memo[*firestore.Client]{})
	return Opened(c, held.(*memo[*firestore.Client]), "Firestore", func() (*firestore.Client, error) {
		return firestore.NewClientWithDatabase(context.Background(), c.Project, database, EmulatorGRPC(c.Endpoint)...)
	})
}

func (c *Clients) ProjectWritableDatabase(tier environment.Tier, project string) string {
	return ProjectWritableDatabase(c.Namespace, tier, project)
}

func (c *Clients) RunsFirestore(tier environment.Tier, project string) (*firestore.Client, error) {
	database := c.ProjectWritableDatabase(tier, project)
	held, _ := c.runStores.LoadOrStore(database, &memo[*firestore.Client]{})
	return Opened(c, held.(*memo[*firestore.Client]), "Firestore", func() (*firestore.Client, error) {
		return firestore.NewClientWithDatabase(context.Background(), c.Project, database, EmulatorGRPC(c.Endpoint)...)
	})
}

func (c *Clients) TagRecordsFirestore(tier environment.Tier, project string) (*firestore.Client, error) {
	database := c.ProjectWritableDatabase(tier, project)
	held, _ := c.tagStores.LoadOrStore(database, &memo[*firestore.Client]{})
	return Opened(c, held.(*memo[*firestore.Client]), "Firestore", func() (*firestore.Client, error) {
		return firestore.NewClientWithDatabase(context.Background(), c.Project, database, append(EmulatorGRPC(c.tagEndpoint()), EmulatorAdmin(c.TagEndpoint)...)...)
	})
}

func ProjectOfRecords(in records.Partition) (string, bool) {
	if in.Root != variablestore.RootValues || len(in.Path) == 0 {
		return "", false
	}
	return in.Path[0], true
}

func ProjectSealedFor(bound seal.AssociatedData) (string, bool) {
	project, owned := bound.Project()
	return project, owned && naming.Validate(projectField, project) == nil
}

type missingProjectPart struct {
	condition error
	refused   error
}

func (m missingProjectPart) Error() string { return m.refused.Error() }

func (m missingProjectPart) Unwrap() error { return m.refused }

func (m missingProjectPart) Is(target error) bool { return target == m.condition }

func refuseNoProjectRecords(tier environment.Tier, project string) error {
	return missingProjectPart{condition: ErrNoProjectRecords, refused: refusal.Refuse(refusal.CodeNotReady,
		"project %s keeps no records database in the %s tier yet, and an app of it reads its values from that database alone.\n"+
			"Deploy %s to the %s tier, or set one of its values with the ocel CLI, which makes it, then try again",
		project, tier, project, tier)}
}

func refuseNoProjectKey(tier environment.Tier, project string) error {
	return missingProjectPart{condition: ErrNoProjectKey, refused: refusal.Refuse(refusal.CodeNotReady,
		"project %s has no key of its own in the %s tier yet, and its values are sealed under that key alone.\n"+
			"Deploy %s to the %s tier, or set one of its values with the ocel CLI, which makes it, then try again",
		project, tier, project, tier)}
}

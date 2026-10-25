package ports

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
)

const (
	sharedDatabaseWord     = "shared"
	ownDatabaseInfix       = "-p-"
	projectHashLength      = 10
	projectDatabasesRecord = "projectdatabases"
	servedCollection       = "served"
)

var (
	ErrNoProjectRecords = errors.New("the project's records database does not exist yet")
	ErrNoTierKey        = errors.New("the tier has no key")
)

func projectHash(tier environment.Tier, project string) string {
	sum := sha256.Sum256([]byte(string(tier) + "\x00" + project))
	return hex.EncodeToString(sum[:])[:projectHashLength]
}

func SharedDatabase(namespace provider.Namespace, tier environment.Tier) string {
	return naming.AppFence(string(namespace)) + naming.TierWord(tier) + naming.WordSeparator + sharedDatabaseWord
}

func OwnDatabasePrefix(namespace provider.Namespace, tier environment.Tier) string {
	return naming.AppFence(string(namespace)) + naming.TierWord(tier) + ownDatabaseInfix
}

func OwnDatabase(namespace provider.Namespace, tier environment.Tier, project string) string {
	return OwnDatabasePrefix(namespace, tier) + projectHash(tier, project)
}

func ProjectDatabaseKey(tier environment.Tier, project string) records.Key {
	return records.Partition{Tier: tier, Root: stackrecords.RootBootstrap}.Key(projectDatabasesRecord, project)
}

type projectDatabaseRecord struct {
	Database string `json:"database"`
}

func ProjectDatabaseRecord(database string) ([]byte, error) {
	return json.Marshal(projectDatabaseRecord{Database: database})
}

func ReadRecordedDatabase(ctx context.Context, store records.Store, namespace provider.Namespace, tier environment.Tier, project string) (string, error) {
	recorded, found, err := ReadProjectDatabaseRecord(ctx, store, tier, project)
	if err != nil || found {
		return recorded, err
	}
	return SharedDatabase(namespace, tier), nil
}

func ReadProjectDatabaseRecord(ctx context.Context, store records.Store, tier environment.Tier, project string) (string, bool, error) {
	entry, err := records.ReadOrEmpty(ctx, store, ProjectDatabaseKey(tier, project))
	if err != nil || len(entry.Value) == 0 {
		return "", false, err
	}
	database, err := DecodeProjectDatabaseRecord(entry)
	return database, err == nil, err
}

func DecodeProjectDatabaseRecord(entry records.Entry) (string, error) {
	var recorded projectDatabaseRecord
	if err := json.Unmarshal(entry.Value, &recorded); err != nil {
		return "", fmt.Errorf("decode %s: %w", entry.Key, err)
	}
	return recorded.Database, nil
}

func ListProjectDatabases(ctx context.Context, store records.Store, tier environment.Tier) (map[string]string, error) {
	entries, err := store.List(ctx, records.Partition{Tier: tier, Root: stackrecords.RootBootstrap}, projectDatabasesRecord)
	if err != nil {
		return nil, err
	}
	recorded := make(map[string]string, len(entries))
	for _, entry := range entries {
		if len(entry.Key.Path) != 2 {
			continue
		}
		database, err := DecodeProjectDatabaseRecord(entry)
		if err != nil {
			return nil, err
		}
		recorded[entry.Key.Path[1]] = database
	}
	return recorded, nil
}

func (c *Clients) SharedDatabase(tier environment.Tier) string {
	return SharedDatabase(c.Namespace, tier)
}

func (c *Clients) OwnDatabase(tier environment.Tier, project string) string {
	return OwnDatabase(c.Namespace, tier, project)
}

func (c *Clients) ReadProjectDatabase(ctx context.Context, tier environment.Tier, project string) (string, error) {
	if database, told := c.ProjectDatabases[project]; told {
		return database, nil
	}
	if own, held := c.ownDatabases.Load(string(tier) + "/" + project); held {
		return own.(string), nil
	}
	if c.Records == nil {
		return "", fmt.Errorf("no database is named for project %s in the %s tier, and no records say which it keeps its records in", project, tier)
	}
	database, err := ReadRecordedDatabase(ctx, c.Records, c.Namespace, tier, project)
	if err != nil {
		return "", err
	}
	if database != c.SharedDatabase(tier) {
		c.ownDatabases.Store(string(tier)+"/"+project, database)
	}
	return database, nil
}

func (c *Clients) ProjectFirestore(ctx context.Context, tier environment.Tier, project string) (*firestore.Client, error) {
	database, err := c.ReadProjectDatabase(ctx, tier, project)
	if err != nil {
		return nil, err
	}
	return c.DatabaseFirestore(database)
}

func (c *Clients) DatabaseFirestore(database string) (*firestore.Client, error) {
	held, _ := c.projectStores.LoadOrStore(database, &memo[*firestore.Client]{})
	return Opened(c, held.(*memo[*firestore.Client]), "Firestore", func() (*firestore.Client, error) {
		return firestore.NewClientWithDatabase(context.Background(), c.Project, database, EmulatorGRPC(c.Endpoint)...)
	})
}

func UntilDatabaseServes(ctx context.Context, c *Clients, database string) error {
	client, err := c.DatabaseFirestore(database)
	if err != nil {
		return err
	}
	err = googleretry.UntilServed(ctx, func() error {
		return client.RunTransaction(ctx, func(_ context.Context, tx *firestore.Transaction) error {
			documents := tx.Documents(client.Collection(servedCollection).Limit(1))
			defer documents.Stop()
			_, err := documents.Next()
			if errors.Is(err, iterator.Done) {
				return nil
			}
			return err
		})
	})
	if err != nil {
		return fmt.Errorf("wait for Firestore to serve the %s database: %w", database, err)
	}
	return nil
}

func (c *Clients) TagRecordsFirestore(ctx context.Context, tier environment.Tier, project string) (*firestore.Client, error) {
	database, err := c.ReadProjectDatabase(ctx, tier, project)
	if err != nil {
		return nil, err
	}
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

type missingProjectPart struct {
	condition error
	refused   error
	answer    error
}

func (m missingProjectPart) Error() string { return m.refused.Error() }

func (m missingProjectPart) Unwrap() []error { return []error{m.refused, m.answer} }

func (m missingProjectPart) Is(target error) bool { return target == m.condition }

func refuseNoProjectRecords(tier environment.Tier, project string, answer error) error {
	return missingProjectPart{condition: ErrNoProjectRecords, answer: answer, refused: refusal.Refuse(refusal.CodeNotReady,
		"the Firestore database project %s keeps its %s records in does not exist yet.\n"+
			"Deploy %s to the %s tier, or set one of its values with the ocel CLI, which makes it, then try again",
		project, tier, project, tier)}
}

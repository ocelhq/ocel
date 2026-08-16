package servicemap

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

const fixtureSecret = "hunter2-do-not-publish"

func fixtureManifest() *deploymentsv1.Manifest {
	return &deploymentsv1.Manifest{
		Slug: "proj-123",
		Resources: []*deploymentsv1.ManifestResource{
			{LogicalName: "db--main", Resource: &resourcesv1.ResourceIdentifier{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, Name: "main"}},
			{LogicalName: "bucket--uploads", Resource: &resourcesv1.ResourceIdentifier{Type: linksv1.LinkType_LINK_TYPE_BUCKET, Name: "uploads"}},
		},
		Usages: []*deploymentsv1.ManifestUsage{
			{App: "api", Resource: "db--main", Files: []string{"apps/api/src/server.ts", "shared/db.ts"}},
			{App: "web", Resource: "db--main", Files: []string{"apps/web/app/page.tsx"}},
		},
	}
}

func fixtureLinks() []*linksv1.Link {
	return []*linksv1.Link{
		{
			Name: "db--main",
			Properties: &linksv1.Link_Postgres{Postgres: &linksv1.PostgresProperties{
				Host:     "db.internal",
				Port:     5432,
				Username: "app",
				Password: fixtureSecret,
				Database: "main",
			}},
			Grants: []*linksv1.Grant{
				{Actions: []string{"fake:connect"}, Resources: []string{"fake:resource/main-" + fixtureSecret}, Label: "connect"},
			},
		},
		{
			Name:       "bucket--uploads",
			Properties: &linksv1.Link_Bucket{Bucket: &linksv1.BucketProperties{Bucket: "proj-123-uploads-" + fixtureSecret}},
			Grants: []*linksv1.Grant{
				{Actions: []string{"fake:get", "fake:put"}, Resources: []string{"fake:resource/uploads-" + fixtureSecret}},
			},
		},
	}
}

func fixtureDeploy() Deploy {
	return Deploy{
		Slug:        "proj-123",
		Environment: Environment{Class: "preview", Identity: "e2e-42"},
		PromotionID: "prm_1",
		Tag:         "v9",
	}
}

func TestDerive(t *testing.T) {
	t.Parallel()

	t.Run("the edges are the manifest's usages", func(t *testing.T) {
		t.Parallel()

		got := Derive(fixtureDeploy(), fixtureManifest(), fixtureLinks())

		want := []Usage{
			{App: "api", Resource: "db--main", Files: []string{"apps/api/src/server.ts", "shared/db.ts"}},
			{App: "web", Resource: "db--main", Files: []string{"apps/web/app/page.tsx"}},
		}
		if !reflect.DeepEqual(got.Usages, want) {
			t.Errorf("usages = %+v, want %+v", got.Usages, want)
		}
	})

	t.Run("an orphan link is a node with no edge", func(t *testing.T) {
		t.Parallel()

		got := Derive(fixtureDeploy(), fixtureManifest(), fixtureLinks())

		names := make([]string, 0, len(got.Links))
		for _, l := range got.Links {
			names = append(names, l.Name)
		}
		if !reflect.DeepEqual(names, []string{"bucket--uploads", "db--main"}) {
			t.Errorf("links = %v, want every link including the unused one", names)
		}
		for _, u := range got.Usages {
			if u.Resource == "bucket--uploads" {
				t.Errorf("usages = %+v, want no edge for the unused link", got.Usages)
			}
		}
	})

	t.Run("var keys and grant verbs sit on the link, never on the edge", func(t *testing.T) {
		t.Parallel()

		got := Derive(fixtureDeploy(), fixtureManifest(), fixtureLinks())

		var db Link
		for _, l := range got.Links {
			if l.Name == "db--main" {
				db = l
			}
		}
		if want := []string{"database", "host", "password", "port", "username"}; !reflect.DeepEqual(db.VarKeys, want) {
			t.Errorf("varKeys = %v, want %v", db.VarKeys, want)
		}
		if want := []Grant{{Verb: "connect", Actions: []string{"fake:connect"}}}; !reflect.DeepEqual(db.Grants, want) {
			t.Errorf("grants = %+v, want %+v", db.Grants, want)
		}

		raw, err := json.Marshal(got.Usages[0])
		if err != nil {
			t.Fatalf("marshal usage: %v", err)
		}
		var edge map[string]any
		if err := json.Unmarshal(raw, &edge); err != nil {
			t.Fatalf("usage is not valid JSON: %v", err)
		}
		for _, key := range []string{"app", "resource", "files"} {
			delete(edge, key)
		}
		if len(edge) != 0 {
			t.Errorf("usage carries %v, want the link's own fields joined rather than copied", edge)
		}
	})

	t.Run("a link the provider reported nothing for is absent", func(t *testing.T) {
		t.Parallel()

		got := Derive(fixtureDeploy(), fixtureManifest(), nil)

		if len(got.Links) != 0 {
			t.Errorf("links = %+v, want none when the deploy reported none", got.Links)
		}
		if len(got.Usages) != 2 {
			t.Errorf("usages = %+v, want the manifest's edges regardless", got.Usages)
		}
	})

	t.Run("carries the deploy context", func(t *testing.T) {
		t.Parallel()

		got := Derive(fixtureDeploy(), fixtureManifest(), fixtureLinks())

		if got.Slug != "proj-123" || got.PromotionID != "prm_1" || got.Tag != "v9" {
			t.Errorf("record = %+v, want the deploy's slug, promotion and tag", got)
		}
		if (got.Environment != Environment{Class: "preview", Identity: "e2e-42"}) {
			t.Errorf("environment = %+v, want the named preview", got.Environment)
		}
	})
}

func TestPublish(t *testing.T) {
	t.Parallel()

	t.Run("no property value reaches the published record", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		if err := Publish(dir, Derive(fixtureDeploy(), fixtureManifest(), fixtureLinks())); err != nil {
			t.Fatalf("Publish() error = %v", err)
		}

		raw, err := os.ReadFile(Path(dir))
		if err != nil {
			t.Fatalf("read service map: %v", err)
		}
		if strings.Contains(string(raw), fixtureSecret) {
			t.Errorf("service map = %s, want no property value anywhere in it", raw)
		}
		if !strings.Contains(string(raw), "password") {
			t.Errorf("service map = %s, want the property key kept", raw)
		}
	})

	t.Run("stamps the schema version and the time", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		if err := Publish(dir, Record{Slug: "proj-123"}); err != nil {
			t.Fatalf("Publish() error = %v", err)
		}

		var got Record
		raw, err := os.ReadFile(Path(dir))
		if err != nil {
			t.Fatalf("read service map: %v", err)
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("service map is not valid JSON: %v", err)
		}
		if got.SchemaVersion != SchemaVersion {
			t.Errorf("schemaVersion = %d, want %d", got.SchemaVersion, SchemaVersion)
		}
		if got.DeployedAt.IsZero() {
			t.Error("deployedAt is zero, want the publication time")
		}
	})
}

func TestClear(t *testing.T) {
	t.Parallel()

	t.Run("tolerates the absence of a map", func(t *testing.T) {
		t.Parallel()
		if err := Clear(t.TempDir()); err != nil {
			t.Fatalf("Clear() on a project with no map error = %v", err)
		}
	})

	t.Run("removes a stale map", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		if err := Publish(dir, Record{Slug: "stale"}); err != nil {
			t.Fatalf("Publish() error = %v", err)
		}
		if err := Clear(dir); err != nil {
			t.Fatalf("Clear() error = %v", err)
		}
		if _, err := os.Stat(Path(dir)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("stat after Clear() = %v, want the file removed", err)
		}
	})
}

func TestPath(t *testing.T) {
	t.Parallel()

	t.Run("is under the project scratch dir", func(t *testing.T) {
		t.Parallel()
		if got, want := Path("/p"), filepath.Join("/p", ".ocel", "service-map.json"); got != want {
			t.Errorf("Path() = %q, want %q", got, want)
		}
	})
}

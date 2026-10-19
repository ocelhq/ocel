package stackrecords_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func writeProject(t *testing.T, store records.Store, tier environment.Tier, slug string, project stackrecords.Project) {
	t.Helper()
	value, err := json.Marshal(project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(context.Background(), records.Entry{Key: stackrecords.ProjectKey(tier, slug), Value: value}); err != nil {
		t.Fatal(err)
	}
}

func TestListProjectsLeavesOutAProjectWhoseOnlyRecordIsItsParts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	tier := environment.TierProduction
	writeProject(t, store, tier, "web", stackrecords.Project{})
	if err := stackrecords.RecordPartsUsed(ctx, store, tier, "web", []string{"image-optimizer"}); err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.RecordPartsUsed(ctx, store, tier, "gone", []string{"image-optimizer"}); err != nil {
		t.Fatal(err)
	}

	projects, err := stackrecords.ListProjects(ctx, store, tier)
	if err != nil {
		t.Fatalf("ListProjects() = %v", err)
	}

	var slugs []string
	for _, project := range projects {
		slugs = append(slugs, project.Slug)
	}
	if !reflect.DeepEqual(slugs, []string{"web"}) {
		t.Errorf("projects = %v, want web alone: gone has only its parts record", slugs)
	}
}

func TestListProjectsReadsTheEdgeRegistryAndLastDeployEachProjectRecorded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	tier := environment.TierPreview
	web := stackrecords.Project{
		Parts: []string{"base"},
		Edge: &stackrecords.ProjectEdge{
			Kind:          "cloudflare",
			DNSKind:       "cloudflare",
			DNSZone:       "example.com",
			AllowDegraded: []string{"waf"},
			Options:       json.RawMessage(`{"account":"a1"}`),
		},
		Registry:  &stackrecords.ProjectRegistry{Server: "ghcr.io", Namespace: "acme", Username: "bot"},
		UpdatedAt: 1700000000,
	}
	writeProject(t, store, tier, "web", web)
	writeProject(t, store, tier, "api", stackrecords.Project{UpdatedAt: 1600000000})
	writeProject(t, store, tier, "docs", stackrecords.Project{})

	projects, err := stackrecords.ListProjects(ctx, store, tier)
	if err != nil {
		t.Fatalf("ListProjects() = %v", err)
	}

	want := []stackrecords.NamedProject{
		{Slug: "api", Project: stackrecords.Project{UpdatedAt: 1600000000}},
		{Slug: "docs"},
		{Slug: "web", Project: web},
	}
	if !reflect.DeepEqual(projects, want) {
		t.Errorf("projects = %+v, want %+v", projects, want)
	}
}

func TestReadProjectOfAProjectNeverRecordedIsAbsent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	tier := environment.TierProduction
	writeProject(t, store, tier, "web", stackrecords.Project{UpdatedAt: 42})

	_, found, err := stackrecords.ReadProject(ctx, store, tier, "docs")
	if err != nil {
		t.Fatalf("ReadProject(docs) = %v", err)
	}
	if found {
		t.Error("ReadProject(docs) found a project that was never recorded")
	}

	web, found, err := stackrecords.ReadProject(ctx, store, tier, "web")
	if err != nil || !found {
		t.Fatalf("ReadProject(web) = %v, %v", found, err)
	}
	if web.UpdatedAt != 42 {
		t.Errorf("web.UpdatedAt = %d, want 42", web.UpdatedAt)
	}
}

func TestDecodeProjectOfAnEmptyValueIsTheZeroProject(t *testing.T) {
	t.Parallel()
	project, err := stackrecords.DecodeProject(records.Entry{Key: stackrecords.ProjectKey(environment.TierProduction, "web")})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(project, stackrecords.Project{}) {
		t.Fatalf("project = %+v, want the zero project", project)
	}
}

func TestDecodeProjectOfAMalformedValueNamesTheKey(t *testing.T) {
	t.Parallel()
	key := stackrecords.ProjectKey(environment.TierProduction, "web")
	_, err := stackrecords.DecodeProject(records.Entry{Key: key, Value: json.RawMessage(`{"parts":`)})
	if err == nil || !strings.Contains(err.Error(), key.String()) {
		t.Fatalf("err = %v, want one naming %s", err, key)
	}
}

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/servicemap"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
)

func TestServiceMap(t *testing.T) {
	t.Run("a deploy publishes a map whose edges are the manifest's usages", func(t *testing.T) {
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, []manifestbuilder.Function{
			{Name: "api", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/api", Framework: "express", App: "api"},
		})
		root, sockPath := setUpDeployFixture(t)
		writeUsageMonorepo(t, root)

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), d, root, deployOptions{yes: true, tag: "v9"}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		got := readServiceMap(t, root)
		want := []servicemap.Usage{{App: "api", Resource: "db--main", Files: []string{"apps/api/src/server.ts"}}}
		if !reflect.DeepEqual(got.Usages, want) {
			t.Errorf("usages = %+v, want %+v", got.Usages, want)
		}
		if got.SchemaVersion != servicemap.SchemaVersion {
			t.Errorf("schemaVersion = %d, want %d", got.SchemaVersion, servicemap.SchemaVersion)
		}
		if got.Slug != "test-app" || got.Environment.Class != "production" || got.PromotionID != fakePromotionID || got.Tag != "v9" {
			t.Errorf("record = %+v, want the deploy's own context", got)
		}
		if got.DeployedAt.IsZero() {
			t.Error("deployedAt is zero, want the publication time")
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("var keys and grant verbs come from the link, and no property value does", func(t *testing.T) {
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, []manifestbuilder.Function{
			{Name: "api", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/api", Framework: "express", App: "api"},
		})
		root, sockPath := setUpDeployFixture(t)
		writeUsageMonorepo(t, root)

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), d, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		raw, err := os.ReadFile(servicemap.Path(root))
		if err != nil {
			t.Fatalf("read service map: %v", err)
		}
		if strings.Contains(string(raw), fakeLinkSecret) {
			t.Errorf("service map = %s, want no property value in it", raw)
		}

		got := readServiceMap(t, root)
		want := []servicemap.Link{{
			Name:    "db--main",
			Type:    linksv1.LinkType_LINK_TYPE_POSTGRES.String(),
			VarKeys: []string{"database", "host", "password", "port", "username"},
			Grants:  []servicemap.Grant{{Verb: "connect", Actions: []string{"fake:connect"}}},
		}}
		if !reflect.DeepEqual(got.Links, want) {
			t.Errorf("links = %+v, want %+v", got.Links, want)
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("a failed deploy leaves no stale map behind", func(t *testing.T) {
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		root, _ := setUpDeployFixture(t)
		if err := servicemap.Publish(root, servicemap.Record{PromotionID: "prm_previous_run"}); err != nil {
			t.Fatalf("seed stale map: %v", err)
		}
		t.Setenv(deployFakeProviderModeEnvVar, "fail")

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), d, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err == nil {
			t.Fatalf("runDeploy err = nil, want the simulated failure; stdout=%s", stdout.String())
		}

		if _, err := os.Stat(servicemap.Path(root)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("stat %s = %v, want no map after a failed deploy", servicemap.Path(root), err)
		}
	})
}

func readServiceMap(t *testing.T, root string) servicemap.Record {
	t.Helper()
	raw, err := os.ReadFile(servicemap.Path(root))
	if err != nil {
		t.Fatalf("read service map: %v", err)
	}
	var got servicemap.Record
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("service map is not valid JSON: %v", err)
	}
	return got
}

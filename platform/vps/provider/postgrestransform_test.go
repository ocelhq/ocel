package vps_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/transformkit"
	"github.com/ocelhq/ocel/pkg/transformkit/transformtest"
)

const patchedImage = "public.ecr.aws/docker/library/postgres@sha256:1f0c2a5b8e3d4c6f7a9b0c1d2e3f405162738495a6b7c8d9e0f1a2b3c4d5e6f7"

type patching struct {
	seen    transformkit.Request
	patches transformkit.Patches
	tags    map[string]string
}

func (a *patching) Evaluate(_ context.Context, req transformkit.Request) ([]transformkit.Result, error) {
	a.seen = req
	return []transformkit.Result{{Patches: a.patches, Tags: a.tags}}, nil
}

func patched(t *testing.T, pass *patching) (*box, provider.Binding, error) {
	t.Helper()
	machine := &box{}
	p := over(machine)
	p.Transforming(pass)
	binding, err := p.ProvisionPostgres(context.Background(), aPostgres(t, "17"), nil)
	return machine, binding, err
}

func TestABoxRendersTransforms(t *testing.T) {
	t.Parallel()

	if !over(&box{}).Facts().RendersTransforms {
		t.Error("a project listing a transform is refused on a box, and a box now has a stack a transform can reshape")
	}
}

func TestATransformIsAskedAboutThePostgresABoxIsAboutToStart(t *testing.T) {
	t.Parallel()

	pass := &patching{}
	if _, _, err := patched(t, pass); err != nil {
		t.Fatalf("Postgres() = %v", err)
	}
	if pass.seen.Provider != "vps" || pass.seen.EnvClass != "production" {
		t.Errorf("the transform was told %q in %q, want the vps branch in production", pass.seen.Provider, pass.seen.EnvClass)
	}
	if len(pass.seen.Resources) != 1 || pass.seen.Resources[0] != (transformkit.Resource{Type: "postgres", Name: "main"}) {
		t.Errorf("the transform was offered %v, want the one postgres being provisioned", pass.seen.Resources)
	}
}

func TestATransformReshapesTheStackAPostgresRunsAs(t *testing.T) {
	t.Parallel()

	machine, _, err := patched(t, &patching{
		patches: transformkit.Patches{
			"container": {
				"image": patchedImage, "args": []any{"-c", "max_connections=200"},
				"env": map[string]any{"POSTGRES_INITDB_ARGS": "--data-checksums"}, "memory": "2g", "cpus": "1.5", "shmSize": "256m",
			},
			"volume": {"driver": "local", "driverOpts": map[string]any{"type": "none", "o": "bind", "device": "/mnt/pg"}},
		},
		tags: map[string]string{"team": "core"},
	})
	if err != nil {
		t.Fatalf("Postgres() = %v", err)
	}
	runCommand := machine.commands()[machine.at("'docker' 'run'")]
	for _, want := range []string{
		"'" + patchedImage + "' '-c' 'max_connections=200'",
		"'--memory' '2g'", "'--cpus' '1.5'", "'--shm-size' '256m'",
		"'--label' 'team=core'",
		"'--network' 'ocel-production-shop'",
	} {
		if !strings.Contains(runCommand, want) {
			t.Errorf("the container was started without %s:\n%s", want, runCommand)
		}
	}
	kept := machine.commands()[machine.at("'docker' 'volume' 'create'")]
	for _, want := range []string{"'--driver' 'local'", "'--opt' 'device=/mnt/pg'", "'--label' 'team=core'"} {
		if !strings.Contains(kept, want) {
			t.Errorf("the volume was created without %s:\n%s", want, kept)
		}
	}
	if !strings.Contains(strings.Join(machine.feeds(), "\n"), "POSTGRES_INITDB_ARGS=--data-checksums") {
		t.Error("the environment a transform added never reached the container")
	}
}

func TestATransformThatWouldBreakWhatAnAppBindsToIsRefusedBeforeTheBoxIsReached(t *testing.T) {
	t.Parallel()

	for name, bad := range map[string]struct {
		patches transformkit.Patches
		want    string
	}{
		"an image a registry can move":       {transformkit.Patches{"container": {"image": "pgvector/pgvector:pg17"}}, "sha256"},
		"the password":                       {transformkit.Patches{"container": {"env": map[string]any{"POSTGRES_PASSWORD": "mine"}}}, "POSTGRES_PASSWORD"},
		"the role an app signs in as":        {transformkit.Patches{"container": {"env": map[string]any{"POSTGRES_USER": "app"}}}, "POSTGRES_USER"},
		"the database an app is bound to":    {transformkit.Patches{"container": {"env": map[string]any{"POSTGRES_DB": "other"}}}, "POSTGRES_DB"},
		"where the data is kept":             {transformkit.Patches{"container": {"env": map[string]any{"PGDATA": "/tmp"}}}, "PGDATA"},
		"a field the box fills itself":       {transformkit.Patches{"container": {"network": "host"}}, "network"},
		"something the box never runs":       {transformkit.Patches{"sidecar": {"image": patchedImage}}, "sidecar"},
		"a binding output a box cannot read": {transformkit.Patches{"container": {"memory": map[string]any{"$ocelOutput": "x"}}}, "memory"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			machine, _, err := patched(t, &patching{patches: bad.patches})
			if err == nil {
				t.Fatal("the transform was rendered")
			}
			if !strings.Contains(err.Error(), bad.want) || !strings.Contains(err.Error(), "main") {
				t.Errorf("the refusal reads %q and never names %s on main", err, bad.want)
			}
			if machine.at("'docker'") >= 0 {
				t.Errorf("the box was reached before the refusal:\n%s", strings.Join(machine.commands(), "\n"))
			}
		})
	}
}

func TestEveryFieldTheVpsBranchTypesIsOneABoxRenders(t *testing.T) {
	t.Parallel()

	root := transformtest.Root(t, map[string]string{
		"stack.transform.ts": `
			import { defineTransform } from "@ocel/transforms"
			export default defineTransform({
				tags: { team: "core" },
				vps: {
					postgres: {
						container: {
							image: "` + patchedImage + `",
							args: ["-c", "max_connections=200"],
							env: { POSTGRES_INITDB_ARGS: "--data-checksums" },
							memory: "2g",
							cpus: "1.5",
							shmSize: "256m",
						},
						volume: { driver: "local", driverOpts: { type: "none", o: "bind", device: "/mnt/pg" } },
					},
				},
			})
		`,
	})
	machine := &box{}
	provider := over(machine)
	provider.Transforming(transformkit.NodePass{Root: root, Modules: []string{"./stack.transform.ts"}})
	if _, err := provider.ProvisionPostgres(context.Background(), aPostgres(t, "17"), nil); err != nil {
		t.Fatalf("a module patching every field the vps branch types was refused: %v", err)
	}
	runCommand := machine.commands()[machine.at("'docker' 'run'")]
	for _, want := range []string{patchedImage, "'--memory' '2g'", "'--cpus' '1.5'", "'--shm-size' '256m'", "'--label' 'team=core'"} {
		if !strings.Contains(runCommand, want) {
			t.Errorf("the module's %s never reached the box:\n%s", want, runCommand)
		}
	}
}

type perResource struct {
	patches map[string]transformkit.Patches
}

func (p *perResource) Evaluate(_ context.Context, req transformkit.Request) ([]transformkit.Result, error) {
	if len(req.Resources) == 0 {
		return nil, nil
	}
	return []transformkit.Result{{Patches: p.patches[req.Resources[0].Name]}}, nil
}

func TestTwoBucketsPatchingTheOneStoreDifferentlyAreRefused(t *testing.T) {
	t.Parallel()

	provider := over(&box{kept: sealedRootKey()})
	provider.Transforming(&perResource{patches: map[string]transformkit.Patches{
		"uploads": {"container": {"memory": "2g"}},
		"avatars": {"container": {"memory": "4g"}},
	}})

	if _, err := provider.ProvisionBucket(context.Background(), aBucket(t, "uploads", false), nil); err != nil {
		t.Fatalf("Bucket(uploads) = %v", err)
	}
	_, err := provider.ProvisionBucket(context.Background(), aBucket(t, "avatars", false), nil)
	if err == nil {
		t.Fatal("the second bucket reshaped the one store the first is already running in, and nothing said so")
	}
	for _, want := range []string{"avatars", "uploads"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal reads %q and never names %s", err, want)
		}
	}
}

func TestTwoBucketsPatchingTheOneStoreTheSameWayProvisionTogether(t *testing.T) {
	t.Parallel()

	provider := over(&box{kept: sealedRootKey()})
	provider.Transforming(&patching{patches: transformkit.Patches{"container": {"memory": "2g"}}})

	if _, err := provider.ProvisionBucket(context.Background(), aBucket(t, "uploads", false), nil); err != nil {
		t.Fatalf("Bucket(uploads) = %v", err)
	}
	if _, err := provider.ProvisionBucket(context.Background(), aBucket(t, "avatars", false), nil); err != nil {
		t.Fatalf("Bucket(avatars) = %v, want two buckets shaping the store alike to be provisioned", err)
	}
}

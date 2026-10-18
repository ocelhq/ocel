package cloudflare

import (
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/gate"
)

func fieldNames(typ reflect.Type) []string {
	names := make([]string, 0, typ.NumField())
	for i := range typ.NumField() {
		names = append(names, typ.Field(i).Name)
	}
	slices.Sort(names)
	return names
}

func TestSpecStampShape(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		typ  reflect.Type
		want []string
	}{
		{
			typ: reflect.TypeFor[edge.StackSpec](),
			want: []string{
				"DomainApps", "Domains", "Gate", "GlobalPreview", "Program", "PruneOnly", "PruneRoutes", "Say", "ServedElsewhere", "Slug", "Tier", "Version", "Warn",
			},
		},
		{
			typ:  reflect.TypeFor[edge.Program](),
			want: []string{"Env", "Origin", "PreviewBaseDomain", "PreviewKey", "ReleasesStore"},
		},
		{
			typ:  reflect.TypeFor[entryProgram](),
			want: []string{"name", "pruneWorkerStem", "store", "worker"},
		},
		{
			typ: reflect.TypeFor[stampedSpec](),
			want: []string{
				"CompatDate", "CompatFlags", "Domains", "Generic", "GenericName",
				"Observability", "PruneOnly", "PruneRoutes", "PruneWorkerStem", "Slug",
				"StoreEndpoint", "StoreScriptName",
			},
		},
		{
			typ:  reflect.TypeFor[workerScript](),
			want: []string{"AssetBinding", "Assets", "CachedEntrypoints", "ClientCertificates", "DurableObjects", "LoaderBinding", "Main", "Modules", "Secrets", "Services", "Variables"},
		},
		{
			typ:  reflect.TypeFor[workerModule](),
			want: []string{"Content", "ContentType", "Name"},
		},
		{
			typ:  reflect.TypeFor[workerAsset](),
			want: []string{"Content", "Path"},
		},
	} {
		t.Run(tc.typ.Name()+" is hashed field for field", func(t *testing.T) {
			t.Parallel()

			got := fieldNames(tc.typ)
			if slices.Equal(got, tc.want) {
				return
			}
			t.Errorf("%s fields = %v, want %v: specStamp hashes this shape by hand, so a field the hash never reaches leaves upToDate true over a stale deploy — fold the new field into stampedSpec, or leave it out on purpose the way Say, Warn and BootstrapCredential are left out, then bring this list back in line",
				tc.typ.Name(), got, tc.want)
		})
	}
}

func TestSpecStampCoversDeployedMetadata(t *testing.T) {
	t.Run("the deployed metadata contains nothing the stamp cannot reach", func(t *testing.T) {
		t.Parallel()

		got := slices.Sorted(maps.Keys(metadataFromMultipart(t, workerScript{Main: testModule()}, "")))
		want := []string{"bindings", "compatibility_date", "compatibility_flags", "main_module", "observability"}
		if !slices.Equal(got, want) {
			t.Errorf("script metadata keys = %v, want %v: putScript sends this metadata, so a key specStamp never hashes leaves upToDate true over a worker that would deploy differently — fold the new key into stampedSpec, then bring this list back in line",
				got, want)
		}
	})

	t.Run("turning observability off restamps the spec", func(t *testing.T) {
		spec := edge.StackSpec{Slug: "acme-web", Version: "v2", Program: programmed()}
		generic, err := genericWorker(spec, entryOf(t, spec), "")
		if err != nil {
			t.Fatalf("genericWorker: %v", err)
		}

		t.Setenv(envObservability, "on")
		on, err := specStamp(spec, entryOf(t, spec), generic)
		if err != nil {
			t.Fatalf("specStamp with observability on: %v", err)
		}

		t.Setenv(envObservability, "off")
		off, err := specStamp(spec, entryOf(t, spec), generic)
		if err != nil {
			t.Fatalf("specStamp with observability off: %v", err)
		}

		if on == off {
			t.Errorf("stamp = %q either way, want %s to move it: the worker deploys with different observability settings", on, envObservability)
		}
	})

	t.Run("a worker with no domain of its own serves the app on workers.dev, and says so", func(t *testing.T) {
		spec := edge.StackSpec{Slug: "acme-web", Version: "v2", Program: programmed()}

		worker, err := genericWorker(spec, entryOf(t, spec), "")
		if err != nil {
			t.Fatalf("genericWorker: %v", err)
		}

		if worker.Variables[genericServesWorkersDevBinding] != "1" {
			t.Errorf("variables = %v, want %s=1: no custom hostname reaches this app", worker.Variables, genericServesWorkersDevBinding)
		}
	})

	t.Run("a worker bound to a domain keeps the app off workers.dev", func(t *testing.T) {
		for name, spec := range map[string]edge.StackSpec{
			"a domain":            {Slug: "acme-web", Domains: []string{"shop.example.com"}, Program: programmed()},
			"a domain served via": {Slug: "acme-web", ServedElsewhere: []string{"shop.example.com"}, Program: programmed()},
		} {
			worker, err := genericWorker(spec, entryOf(t, spec), "")
			if err != nil {
				t.Fatalf("%s: genericWorker: %v", name, err)
			}
			if _, set := worker.Variables[genericServesWorkersDevBinding]; set {
				t.Errorf("%s: variables = %v, want no %s: the worker answers workers.dev only for purges", name, worker.Variables, genericServesWorkersDevBinding)
			}
		}
	})

	t.Run("a gate does not restamp the spec: it is published to the releases store, not deployed in the script", func(t *testing.T) {
		spec := edge.StackSpec{Slug: "acme-web", Version: "v2", Program: programmed()}
		generic, err := genericWorker(spec, entryOf(t, spec), "")
		if err != nil {
			t.Fatalf("genericWorker: %v", err)
		}
		stampOf := func(published *gate.Gate) string {
			spec.Gate = published
			stamp, err := specStamp(spec, entryOf(t, spec), generic)
			if err != nil {
				t.Fatalf("specStamp: %v", err)
			}
			return stamp
		}

		if ungated, gated := stampOf(nil), stampOf(&gate.Gate{Key: "k1", PasswordMAC: "m1"}); ungated != gated {
			t.Errorf("stamps = %q, %q, want one: a rotated gate must not redeploy the worker, and one that never reaches the script cannot leave it stale", ungated, gated)
		}
	})

	t.Run("binding a client certificate restamps the spec", func(t *testing.T) {
		spec := edge.StackSpec{Slug: "acme-web", Version: "v2", Program: programmed()}
		plain, err := genericWorker(spec, entryOf(t, spec), "")
		if err != nil {
			t.Fatalf("genericWorker: %v", err)
		}
		before, err := specStamp(spec, entryOf(t, spec), plain)
		if err != nil {
			t.Fatalf("specStamp: %v", err)
		}

		spec.Program.Origin = certifiedReach()
		bound, err := genericWorker(spec, entryOf(t, spec), "")
		if err != nil {
			t.Fatalf("genericWorker with a certificate: %v", err)
		}
		after, err := specStamp(spec, entryOf(t, spec), bound)
		if err != nil {
			t.Fatalf("specStamp with a certificate: %v", err)
		}

		if before == after {
			t.Errorf("stamp = %q either way, want a bound client certificate to move it", before)
		}
	})
}

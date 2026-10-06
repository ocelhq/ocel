package cloudflare

import (
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
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
				"DomainApps", "Domains", "Program", "PruneOnly", "PruneRoutes", "ServedElsewhere", "Slug", "Tier", "Values", "Version", "Warn",
			},
		},
		{
			typ: reflect.TypeFor[edge.ProgramSpec](),
			want: []string{
				"BootstrapCredential", "ISRWriterScriptName", "Name", "PruneWorkerStem", "RequiredRecord",
				"StoreEndpoint", "StoreScriptName", "Worker",
			},
		},
		{
			typ: reflect.TypeFor[stampedSpec](),
			want: []string{
				"CompatDate", "CompatFlags", "Domains", "Generic", "GenericName", "ISRWriterScriptName",
				"Observability", "PruneOnly", "PruneRoutes", "PruneWorkerStem", "RequiredRecord", "Slug",
				"StoreEndpoint", "StoreScriptName", "Values",
			},
		},
		{
			typ:  reflect.TypeFor[edge.Worker](),
			want: []string{"AssetBinding", "Assets", "ClientCertificates", "LoaderBinding", "Main", "Modules", "ObjectStore", "Queues", "Secrets", "Services", "Variables"},
		},
		{
			typ:  reflect.TypeFor[edge.WorkerModule](),
			want: []string{"Content", "ContentType", "Name"},
		},
		{
			typ:  reflect.TypeFor[edge.StaticAsset](),
			want: []string{"Content", "Path"},
		},
		{
			typ:  reflect.TypeFor[edge.ObjectStore](),
			want: []string{"Binding", "Bucket"},
		},
	} {
		t.Run(tc.typ.Name()+" is hashed field for field", func(t *testing.T) {
			t.Parallel()

			got := fieldNames(tc.typ)
			if slices.Equal(got, tc.want) {
				return
			}
			t.Errorf("%s fields = %v, want %v: specStamp hashes this shape by hand, so a field the hash never reaches leaves upToDate true over a stale deploy — fold the new field into stampedSpec, or leave it out on purpose the way Warn and BootstrapCredential are left out, then bring this list back in line",
				tc.typ.Name(), got, tc.want)
		})
	}
}

func TestSpecStampCoversDeployedMetadata(t *testing.T) {
	t.Run("the deployed metadata contains nothing the stamp cannot reach", func(t *testing.T) {
		t.Parallel()

		got := slices.Sorted(maps.Keys(metadataFromMultipart(t, edge.Worker{Main: mainModule()}, "")))
		want := []string{"bindings", "compatibility_date", "compatibility_flags", "main_module", "observability"}
		if !slices.Equal(got, want) {
			t.Errorf("script metadata keys = %v, want %v: putScript sends this metadata, so a key specStamp never hashes leaves upToDate true over a worker that would deploy differently — fold the new key into stampedSpec, then bring this list back in line",
				got, want)
		}
	})

	t.Run("turning observability off restamps the spec", func(t *testing.T) {
		spec := edge.StackSpec{Slug: "acme-web", Version: "v2", Program: &edge.ProgramSpec{Name: "ocel-web"}}
		generic, err := genericWorker(spec, spec.Slug, "")
		if err != nil {
			t.Fatalf("genericWorker: %v", err)
		}

		t.Setenv(envObservability, "on")
		on, err := specStamp(spec, generic)
		if err != nil {
			t.Fatalf("specStamp with observability on: %v", err)
		}

		t.Setenv(envObservability, "off")
		off, err := specStamp(spec, generic)
		if err != nil {
			t.Fatalf("specStamp with observability off: %v", err)
		}

		if on == off {
			t.Errorf("stamp = %q either way, want %s to move it: the worker deploys with different observability settings", on, envObservability)
		}
	})

	t.Run("binding a queue restamps the spec", func(t *testing.T) {
		spec := edge.StackSpec{Slug: "acme-web", Version: "v2", Program: &edge.ProgramSpec{Name: "ocel-web"}}
		plain, err := genericWorker(spec, spec.Slug, "")
		if err != nil {
			t.Fatalf("genericWorker: %v", err)
		}
		before, err := specStamp(spec, plain)
		if err != nil {
			t.Fatalf("specStamp: %v", err)
		}

		spec.Program.Worker.Queues = map[string]string{refreshQueueBinding: "ocel-refresh"}
		bound, err := genericWorker(spec, spec.Slug, "")
		if err != nil {
			t.Fatalf("genericWorker with a queue: %v", err)
		}
		after, err := specStamp(spec, bound)
		if err != nil {
			t.Fatalf("specStamp with a queue: %v", err)
		}

		if before == after {
			t.Errorf("stamp = %q either way, want a bound queue to move it", before)
		}
	})

	t.Run("binding a client certificate restamps the spec", func(t *testing.T) {
		spec := edge.StackSpec{Slug: "acme-web", Version: "v2", Program: &edge.ProgramSpec{Name: "ocel-web"}}
		plain, err := genericWorker(spec, spec.Slug, "")
		if err != nil {
			t.Fatalf("genericWorker: %v", err)
		}
		before, err := specStamp(spec, plain)
		if err != nil {
			t.Fatalf("specStamp: %v", err)
		}

		spec.Program.Worker.ClientCertificates = map[string]string{edge.OriginClientCertificateBinding: "c1"}
		bound, err := genericWorker(spec, spec.Slug, "")
		if err != nil {
			t.Fatalf("genericWorker with a certificate: %v", err)
		}
		after, err := specStamp(spec, bound)
		if err != nil {
			t.Fatalf("specStamp with a certificate: %v", err)
		}

		if before == after {
			t.Errorf("stamp = %q either way, want a bound client certificate to move it", before)
		}
	})
}

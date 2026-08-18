package cloudflare

import (
	"maps"
	"reflect"
	"slices"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
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
				"Class", "Domains", "Program", "PruneOnly", "PruneRoutes", "Slug", "Values", "Version", "Warn",
			},
		},
		{
			typ: reflect.TypeFor[edge.ProgramSpec](),
			want: []string{
				"BootstrapCred", "ISRWriterScriptName", "Name", "PruneWorkerStem", "RequiredRecord",
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
			want: []string{"AssetBinding", "Assets", "LoaderBinding", "Main", "Modules", "ObjectStore", "Secrets", "Services", "Vars"},
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
			t.Errorf("%s fields = %v, want %v: specStamp hashes this shape by hand, so a field the hash never reaches leaves upToDate true over a stale deploy — fold the new field into stampedSpec, or leave it out on purpose the way Warn and BootstrapCred are left out, then bring this list back in line",
				tc.typ.Name(), got, tc.want)
		})
	}
}

func TestSpecStampCoversDeployedMetadata(t *testing.T) {
	t.Run("the deployed metadata carries nothing the stamp cannot reach", func(t *testing.T) {
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
		generic := genericWorker(spec, spec.Slug)

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
}

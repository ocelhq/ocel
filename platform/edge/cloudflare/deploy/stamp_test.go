package cloudflare

import (
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
			typ: reflect.TypeFor[edge.RootStackSpec](),
			want: []string{
				"BootstrapCred", "Domains", "Generic", "GenericName", "ISRWriterScriptName",
				"PruneRoutes", "PruneWorkerStem", "RequiredRecord", "Slug", "StoreEndpoint",
				"StoreScriptName", "Values", "Version", "Warn",
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

package naming

import (
	"reflect"
	"slices"
	"testing"

	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestKindOf(t *testing.T) {
	for _, tc := range []struct {
		typ  linksv1.LinkType
		want Kind
		ok   bool
	}{
		{linksv1.LinkType_LINK_TYPE_POSTGRES, KindDatabase, true},
		{linksv1.LinkType_LINK_TYPE_BUCKET, KindBucket, true},
		{linksv1.LinkType_LINK_TYPE_UNSPECIFIED, "", false},
		{linksv1.LinkType(99), "", false},
	} {
		got, ok := KindOf(tc.typ)
		if ok != tc.ok || got != tc.want {
			t.Errorf("KindOf(%v) = %q, %v, want %q, %v", tc.typ, got, ok, tc.want, tc.ok)
		}
	}
}

func TestCrossesMembrane(t *testing.T) {
	for _, tc := range []struct {
		typ  linksv1.LinkType
		want bool
	}{
		{linksv1.LinkType_LINK_TYPE_BUCKET, true},
		{linksv1.LinkType_LINK_TYPE_POSTGRES, false},
		{linksv1.LinkType_LINK_TYPE_UNSPECIFIED, false},
		{linksv1.LinkType(99), false},
	} {
		if got := CrossesMembrane(tc.typ); got != tc.want {
			t.Errorf("CrossesMembrane(%v) = %v, want %v", tc.typ, got, tc.want)
		}
	}
}

func TestEnvFragment(t *testing.T) {
	for _, tc := range []struct {
		typ  linksv1.LinkType
		want string
	}{
		{linksv1.LinkType_LINK_TYPE_POSTGRES, "POSTGRES"},
		{linksv1.LinkType_LINK_TYPE_BUCKET, "BUCKET"},
	} {
		if got := EnvFragment(tc.typ); got != tc.want {
			t.Errorf("EnvFragment(%v) = %q, want %q", tc.typ, got, tc.want)
		}
	}
}

func TestEveryLinkTypeOcelProvisionsHasAKind(t *testing.T) {
	for name, value := range linksv1.LinkType_value {
		typ := linksv1.LinkType(value)
		if typ == linksv1.LinkType_LINK_TYPE_UNSPECIFIED || typ == linksv1.LinkType_LINK_TYPE_CUSTOM {
			continue
		}
		if _, ok := KindOf(typ); !ok {
			t.Errorf("%s has no naming kind", name)
		}
	}
	if _, ok := KindOf(linksv1.LinkType_LINK_TYPE_CUSTOM); ok {
		t.Error("LINK_TYPE_CUSTOM has a naming kind, which would let a resource declaration name one ocel never provisions")
	}
}

func TestLinkTypeOf(t *testing.T) {
	for _, tc := range []struct {
		link *linksv1.Link
		want linksv1.LinkType
	}{
		{&linksv1.Link{Properties: &linksv1.Link_Postgres{Postgres: &linksv1.PostgresProperties{}}}, linksv1.LinkType_LINK_TYPE_POSTGRES},
		{&linksv1.Link{Properties: &linksv1.Link_Bucket{Bucket: &linksv1.BucketProperties{}}}, linksv1.LinkType_LINK_TYPE_BUCKET},
		{&linksv1.Link{Properties: &linksv1.Link_Custom{Custom: &structpb.Struct{}}}, linksv1.LinkType_LINK_TYPE_CUSTOM},
		{&linksv1.Link{}, linksv1.LinkType_LINK_TYPE_UNSPECIFIED},
		{nil, linksv1.LinkType_LINK_TYPE_UNSPECIFIED},
	} {
		if got := LinkTypeOf(tc.link); got != tc.want {
			t.Errorf("LinkTypeOf(%v) = %v, want %v", tc.link, got, tc.want)
		}
	}
}

func TestLinkProperties(t *testing.T) {
	link := &linksv1.Link{Properties: &linksv1.Link_Postgres{Postgres: &linksv1.PostgresProperties{Host: "h", Port: 5433}}}
	if got := LinkPropertyNames(link); !slices.Equal(got, []string{"database", "host", "password", "port", "username"}) {
		t.Errorf("LinkPropertyNames = %v", got)
	}
	if got, ok := LinkProperty(link, "port"); !ok || got != float64(5433) {
		t.Errorf("LinkProperty(port) = %v, %v", got, ok)
	}
	if got, ok := LinkProperty(link, "host"); !ok || got != "h" {
		t.Errorf("LinkProperty(host) = %v, %v", got, ok)
	}
	if _, ok := LinkProperty(link, "bucket"); ok {
		t.Error("LinkProperty(bucket) found on a postgres link")
	}
	if got := LinkPropertyNames(&linksv1.Link{}); got != nil {
		t.Errorf("LinkPropertyNames(typeless) = %v, want nil", got)
	}
}

func TestCustomLinkProperties(t *testing.T) {
	custom, err := structpb.NewStruct(map[string]any{
		"subnetIds": []any{"subnet-a", "subnet-b"},
		"vpcId":     "vpc-1",
		"attached":  true,
		"maxConns":  float64(20),
	})
	if err != nil {
		t.Fatalf("build the published struct: %v", err)
	}
	link := &linksv1.Link{Properties: &linksv1.Link_Custom{Custom: custom}}

	if got := LinkPropertyNames(link); !slices.Equal(got, []string{"attached", "maxConns", "subnetIds", "vpcId"}) {
		t.Errorf("LinkPropertyNames = %v", got)
	}
	if got, ok := LinkProperty(link, "subnetIds"); !ok || !reflect.DeepEqual(got, []any{"subnet-a", "subnet-b"}) {
		t.Errorf("LinkProperty(subnetIds) = %v, %v", got, ok)
	}
	for name, want := range map[string]any{"vpcId": "vpc-1", "attached": true, "maxConns": float64(20)} {
		if got, ok := LinkProperty(link, name); !ok || got != want {
			t.Errorf("LinkProperty(%s) = %v, %v, want %v", name, got, ok, want)
		}
	}
	if _, ok := LinkProperty(link, "host"); ok {
		t.Error("LinkProperty(host) found on a record that carries no such key")
	}
}

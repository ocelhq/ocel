package naming

import (
	"slices"
	"testing"

	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
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

func TestEveryLinkTypeHasAKind(t *testing.T) {
	for name, value := range linksv1.LinkType_value {
		typ := linksv1.LinkType(value)
		if typ == linksv1.LinkType_LINK_TYPE_UNSPECIFIED {
			continue
		}
		if _, ok := KindOf(typ); !ok {
			t.Errorf("%s has no naming kind", name)
		}
	}
}

func TestLinkTypeOf(t *testing.T) {
	for _, tc := range []struct {
		link *linksv1.Link
		want linksv1.LinkType
	}{
		{&linksv1.Link{Properties: &linksv1.Link_Postgres{Postgres: &linksv1.PostgresProperties{}}}, linksv1.LinkType_LINK_TYPE_POSTGRES},
		{&linksv1.Link{Properties: &linksv1.Link_Bucket{Bucket: &linksv1.BucketProperties{}}}, linksv1.LinkType_LINK_TYPE_BUCKET},
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
	if got, ok := LinkProperty(link, "port"); !ok || got != "5433" {
		t.Errorf("LinkProperty(port) = %q, %v", got, ok)
	}
	if _, ok := LinkProperty(link, "bucket"); ok {
		t.Error("LinkProperty(bucket) found on a postgres link")
	}
	if got := LinkPropertyNames(&linksv1.Link{}); got != nil {
		t.Errorf("LinkPropertyNames(typeless) = %v, want nil", got)
	}
}

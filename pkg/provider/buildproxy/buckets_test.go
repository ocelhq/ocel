package buildproxy_test

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/buildproxy"
)

func TestTheBucketNamesOfAGrantAreListedOnceEachInBindingOrder(t *testing.T) {
	bucket := func(name, bucket string) provider.Binding {
		return provider.Binding{Type: provider.BindingBucket, Name: name, Properties: map[string]string{provider.PropertyBucket: bucket}}
	}

	names := buildproxy.ListBucketNames(grant("web", bucket("bucket--uploads", "shop-uploads"), bucket("bucket--assets", "shop-assets"), bucket("bucket--media", "shop-uploads")))

	if !slices.Equal(names, []string{"shop-uploads", "shop-assets"}) {
		t.Errorf("ListBucketNames() = %v, want shop-uploads then shop-assets", names)
	}
}

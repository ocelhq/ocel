package devserver

import (
	"slices"
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func TestResourceKindsListsEachDeclaredKindOnceAndNoNames(t *testing.T) {
	s := newDevServer(&fakeResources{})
	url := serve(t, s)
	declareResource(t, url, "orders-db", resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES)
	declareResource(t, url, "uploads", resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET)
	declareResource(t, url, "audit-db", resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES)

	if got, want := s.ResourceKinds(), []string{"bucket", "postgres"}; !slices.Equal(slices.Sorted(slices.Values(got)), want) {
		t.Errorf("ResourceKinds() = %v, want %v", got, want)
	}
}

func TestResourceKindsIsEmptyOnceDeclarationsAreReset(t *testing.T) {
	s := newDevServer(&fakeResources{})
	url := serve(t, s)
	declareResource(t, url, "orders-db", resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES)

	s.ResetDeclarations()

	if got := s.ResourceKinds(); len(got) != 0 {
		t.Errorf("ResourceKinds() = %v, want none", got)
	}
}

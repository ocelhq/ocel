package registry

import "testing"

func TestAnImageInAnECRRegistryNamesItsRepositoryAndRegion(t *testing.T) {
	t.Parallel()

	for _, imageRef := range []string{
		"123456789012.dkr.ecr.eu-west-1.amazonaws.com/ocel/shop.web:sha256-abc",
		"123456789012.dkr.ecr.eu-west-1.amazonaws.com/ocel/shop.web@sha256:0123",
	} {
		name, region, found := ParseImageRepository("ocel", imageRef)
		if !found || name != "ocel/shop.web" || region != "eu-west-1" {
			t.Errorf("ParseImageRepository(%s) = %q, %q, %v, want ocel/shop.web in eu-west-1", imageRef, name, region, found)
		}
	}
}

func TestAnImageOutsideOcelsECRRepositoriesNamesNone(t *testing.T) {
	t.Parallel()

	for _, imageRef := range []string{
		"ghcr.io/acme/shop:1",
		"123456789012.dkr.ecr.eu-west-1.amazonaws.com/team/shop:1",
		"ocel/shop/web:local",
		"123456789012.dkr.ecr.eu-west-1.amazonaws.com/acme/shop.web:sha256-abc",
	} {
		if name, region, found := ParseImageRepository("ocel", imageRef); found {
			t.Errorf("ParseImageRepository(%s) = %q, %q, want none: Ocel created no repository for it", imageRef, name, region)
		}
	}
}

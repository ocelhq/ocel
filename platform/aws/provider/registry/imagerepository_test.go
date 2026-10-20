package registry

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
)

func TestAnImageInAnECRRegistryNamesItsRepositoryAndRegion(t *testing.T) {
	t.Parallel()

	for _, imageRef := range []string{
		"123456789012.dkr.ecr.eu-west-1.amazonaws.com/ocel/prod/shop.web:sha256-abc",
		"123456789012.dkr.ecr.eu-west-1.amazonaws.com/ocel/prod/shop.web@sha256:0123",
	} {
		name, project, region, found := ParseImageRepository("ocel", environment.TierProduction, imageRef)
		if !found || name != "ocel/prod/shop.web" || project != "shop" || region != "eu-west-1" {
			t.Errorf("ParseImageRepository(%s) = %q, %q, %q, %v, want ocel/prod/shop.web of project shop in eu-west-1", imageRef, name, project, region, found)
		}
	}
}

func TestAnImageOutsideOcelsECRRepositoriesOfTheTierNamesNone(t *testing.T) {
	t.Parallel()

	for _, imageRef := range []string{
		"ghcr.io/acme/shop:1",
		"123456789012.dkr.ecr.eu-west-1.amazonaws.com/team/shop:1",
		"ocel/shop/web:local",
		"123456789012.dkr.ecr.eu-west-1.amazonaws.com/acme/prod/shop.web:sha256-abc",
		"123456789012.dkr.ecr.eu-west-1.amazonaws.com/ocel/preview/shop.web:sha256-abc",
	} {
		if name, _, region, found := ParseImageRepository("ocel", environment.TierProduction, imageRef); found {
			t.Errorf("ParseImageRepository(%s) = %q, %q, want none: Ocel created no repository of the production tier for it", imageRef, name, region)
		}
	}
}

package officialimages_test

import (
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/officialimages"
)

const pinnedCaddy = "caddy@sha256:df7f1c2fb114453b951de51a98efc010db1655a92c2e86be6706714e2417a78d"

func TestAnOfficialImageIsPulledFromECRPublicWhenNoMirrorIsNamed(t *testing.T) {
	t.Setenv(officialimages.MirrorEnv, "")

	if got, want := officialimages.QualifyImage(pinnedCaddy), "public.ecr.aws/docker/library/"+pinnedCaddy; got != want {
		t.Errorf("QualifyImage(%q) = %q, want %q", pinnedCaddy, got, want)
	}
}

func TestAnOfficialImageIsPulledFromTheNamedMirrorByTheSameDigest(t *testing.T) {
	for _, mirror := range []string{"mirror.gcr.io/library", "mirror.gcr.io/library/"} {
		t.Setenv(officialimages.MirrorEnv, mirror)

		if got, want := officialimages.QualifyImage(pinnedCaddy), "mirror.gcr.io/library/"+pinnedCaddy; got != want {
			t.Errorf("with %s=%q, QualifyImage(%q) = %q, want %q", officialimages.MirrorEnv, mirror, pinnedCaddy, got, want)
		}
	}
}

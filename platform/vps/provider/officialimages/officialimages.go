package officialimages

import (
	"os"
	"strings"
)

const (
	registry  = "public.ecr.aws/docker/library"
	MirrorEnv = "OCEL_VPS_OFFICIAL_IMAGES_MIRROR"
)

func QualifyImage(image string) string {
	from := registry
	if mirror := strings.TrimSuffix(os.Getenv(MirrorEnv), "/"); mirror != "" {
		from = mirror
	}
	return from + "/" + image
}

package imagebuild

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
)

const repositoryPrefix = "ocel/"

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type Image struct {
	Repository string
	Tag        string
	Digest     string
	Ref        string
}

func imageFor(app, digest string) (Image, error) {
	if !digestPattern.MatchString(digest) {
		return Image{}, fmt.Errorf("the build of %q produced %q where its image's digest belongs, so the image has no coordinate to be released under", app, digest)
	}
	repository := repositoryPrefix + naming.Sanitize(app)
	return Image{
		Repository: repository,
		Tag:        strings.Replace(digest, ":", "-", 1),
		Digest:     digest,
		Ref:        repository + "@" + digest,
	}, nil
}

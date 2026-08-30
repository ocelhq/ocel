package imagebuild

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
)

const repositoryPrefix = "ocel/"

const maxRepository = 255

var (
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	namePattern   = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*$`)
)

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
	name := naming.Sanitize(app)
	repository := repositoryPrefix + name
	if !namePattern.MatchString(name) || len(repository) > maxRepository {
		return Image{}, fmt.Errorf("%q names an image repository of %q, which docker cannot hold: name the app something a repository can be derived from", app, repository)
	}
	return Image{
		Repository: repository,
		Tag:        strings.Replace(digest, ":", "-", 1),
		Digest:     digest,
		Ref:        repository + "@" + digest,
	}, nil
}

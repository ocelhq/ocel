package imagebuild

import (
	"fmt"
	"regexp"

	"github.com/ocelhq/ocel/pkg/naming"
)

const LocalNamespace = "ocel"

const maxRepository = 255

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type Image struct {
	Name       string
	Repository string
	Tag        string
	Digest     string
	Ref        string
}

func Repository(app string) (string, error) {
	name := naming.Sanitize(app)
	if !naming.IsRepositorySegment(name) || len(LocalNamespace)+len("/")+len(name) > maxRepository {
		return "", fmt.Errorf("%q names an image repository of %q, which docker cannot hold: name the app something a repository can be derived from", app, name)
	}
	return name, nil
}

func imageFor(app, digest string) (Image, error) {
	if !digestPattern.MatchString(digest) {
		return Image{}, fmt.Errorf("the build of %q produced %q where its image's digest belongs, so the image has no coordinate to be released under", app, digest)
	}
	name, err := Repository(app)
	if err != nil {
		return Image{}, err
	}
	repository := LocalNamespace + "/" + name
	return Image{
		Name:       name,
		Repository: repository,
		Tag:        naming.DigestTag(digest),
		Digest:     digest,
		Ref:        repository + "@" + digest,
	}, nil
}

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

func Repository(slug, app string) (string, error) {
	project := naming.Sanitize(slug)
	name := naming.Sanitize(app)
	repository := LocalNamespace + "/" + project + "/" + name
	if !naming.IsRepositorySegment(project) || !naming.IsRepositorySegment(name) || len(repository) > maxRepository {
		return "", fmt.Errorf("project %q and app %q name an image repository of %q, which docker cannot hold: name them something a repository can be derived from", slug, app, repository)
	}
	return repository, nil
}

func imageFor(slug, app, digest string) (Image, error) {
	if !digestPattern.MatchString(digest) {
		return Image{}, fmt.Errorf("the build of %q produced %q where its image's digest belongs, so the image has no coordinate to be released under", app, digest)
	}
	repository, err := Repository(slug, app)
	if err != nil {
		return Image{}, err
	}
	return Image{
		Name:       naming.RepositorySegment(repository),
		Repository: repository,
		Tag:        naming.DigestTag(digest),
		Digest:     digest,
		Ref:        repository + "@" + digest,
	}, nil
}

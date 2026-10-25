package host

import (
	"io/fs"
	"path"
	"strings"

	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const nextContainerDir = "dist/next-container"

func NextContainerRuntime(goarch string) (map[string][]byte, error) {
	sharp := path.Join(nextContainerDir, "sharp", goarch)
	if _, err := fs.Stat(helpers, sharp); err != nil {
		return nil, refusal.Refuse(refusal.CodeInvalid, "no box runs %s: build for amd64 or arm64", goarch)
	}
	entrypoint, err := helpers.ReadFile(path.Join(nextContainerDir, images.RuntimeEntrypointFile))
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{images.RuntimeEntrypointFile: entrypoint}
	err = fs.WalkDir(helpers, sharp, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		body, err := helpers.ReadFile(name)
		if err != nil {
			return err
		}
		files[strings.TrimPrefix(name, sharp+"/")] = body
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

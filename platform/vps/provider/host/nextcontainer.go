package host

import (
	"io/fs"
	"path"
	"strings"

	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const nextContainerDir = "dist/next-container"

func NextContainerRuntime(assets Assets, goarch string) (map[string][]byte, error) {
	sharp := path.Join(nextContainerDir, "sharp", goarch)
	if err := assets.stat(sharp); err != nil {
		return nil, refusal.Refuse(refusal.CodeInvalid, "no box runs %s: build for amd64 or arm64", goarch)
	}
	entrypoint, err := assets.read(path.Join(nextContainerDir, images.RuntimeEntrypointFile))
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{images.RuntimeEntrypointFile: entrypoint}
	err = fs.WalkDir(assets.files, sharp, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		body, err := assets.read(name)
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

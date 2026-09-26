package appbuild

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/pkg/constants"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	artifactRootDir = constants.ProjectStateDirName + "/output"

	appsDir = "apps"
)

func ArtifactRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return artifactRootDir
	}
	return filepath.Join(wd, artifactRootDir)
}

func AppArtifactRoot(root, app string) string { return filepath.Join(root, appsDir, app) }

func ReadServeDescriptor(root, app string) (edge.ServeDescriptor, bool, error) {
	raw, err := os.ReadFile(filepath.Join(AppArtifactRoot(root, app), edge.ServeDescriptorFile))
	if errors.Is(err, fs.ErrNotExist) {
		return edge.ServeDescriptor{}, false, nil
	}
	if err != nil {
		return edge.ServeDescriptor{}, false, fmt.Errorf("read serve descriptor for %s: %w", app, err)
	}
	var desc edge.ServeDescriptor
	if err := json.Unmarshal(raw, &desc); err != nil {
		return edge.ServeDescriptor{}, false, fmt.Errorf("parse serve descriptor for %s: %w", app, err)
	}
	return desc, true, nil
}

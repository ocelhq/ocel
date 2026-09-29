package build

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/appbuild"
	"github.com/ocelhq/ocel/pkg/naming"
)

const deploymentIDFileName = "deployment-id"

func mintDeploymentID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("mint deployment id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func deploymentIDRel(app string) string {
	return filepath.Join(appbuild.AppArtifactRoot(filepath.FromSlash(appbuild.ArtifactRootDir), app), deploymentIDFileName)
}

func deploymentIDPath(projectDir, app string) string {
	return filepath.Join(projectDir, deploymentIDRel(app))
}

func writeDeploymentID(projectDir, app, id string) error {
	path := deploymentIDPath(projectDir, app)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(id+"\n"), 0o644); err != nil {
		return fmt.Errorf("record deployment id at %s: %w", path, err)
	}
	return nil
}

func DeploymentID(projectDir, app string) (string, error) {
	path := deploymentIDPath(projectDir, app)
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("no deployment id for app %q at %s; run `ocel build`", app, deploymentIDRel(app))
	}
	if err != nil {
		return "", fmt.Errorf("read deployment id at %s: %w", path, err)
	}
	id := strings.TrimSpace(string(raw))
	if err := naming.ValidateDeploymentID(id); err != nil {
		return "", fmt.Errorf("%s: %w; re-run `ocel build`", path, err)
	}
	return id, nil
}

func recordDeploymentIDs(cfg *project.Project, apps []project.App) (map[string]string, error) {
	ids := make(map[string]string, len(apps))
	for _, a := range apps {
		id, err := mintDeploymentID()
		if err != nil {
			return nil, err
		}
		if err := writeDeploymentID(cfg.Dir, a.Name, id); err != nil {
			return nil, err
		}
		ids[a.Name] = id
	}
	return ids, nil
}

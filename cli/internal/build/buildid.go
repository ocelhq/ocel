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
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/naming"
)

const buildIDFileName = "build-id"

func mintBuildID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("mint build id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func buildIDRel(app string) string {
	return filepath.Join(buildoutput.AppRoot(filepath.FromSlash(buildoutput.Dir), app), buildIDFileName)
}

func buildIDPath(projectDir, app string) string {
	return filepath.Join(projectDir, buildIDRel(app))
}

func writeBuildID(projectDir, app, id string) error {
	path := buildIDPath(projectDir, app)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(id+"\n"), 0o644); err != nil {
		return fmt.Errorf("record build id at %s: %w", path, err)
	}
	return nil
}

func BuildID(projectDir, app string) (string, error) {
	path := buildIDPath(projectDir, app)
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("no build id for app %q at %s; run `ocel build`", app, buildIDRel(app))
	}
	if err != nil {
		return "", fmt.Errorf("read build id at %s: %w", path, err)
	}
	id := strings.TrimSpace(string(raw))
	if err := naming.ValidateBuildID(id); err != nil {
		return "", fmt.Errorf("%s: %w; re-run `ocel build`", path, err)
	}
	return id, nil
}

func recordBuildIDs(cfg *project.Project, apps []project.App) (map[string]string, error) {
	ids := make(map[string]string, len(apps))
	for _, a := range apps {
		id, err := mintBuildID()
		if err != nil {
			return nil, err
		}
		if err := writeBuildID(cfg.Dir, a.Name, id); err != nil {
			return nil, err
		}
		ids[a.Name] = id
	}
	return ids, nil
}

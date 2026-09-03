package deployresult

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const SchemaVersion = 1

const scratchDirName = ".ocel"

const fileName = "deploy-result.json"

type Result struct {
	SchemaVersion int         `json:"schemaVersion"`
	Slug          string      `json:"slug"`
	Environment   Environment `json:"environment"`
	PromotionID   string      `json:"promotionId"`
	Tag           string      `json:"tag,omitempty"`
	Apps          []App       `json:"apps"`
	DeployedAt    time.Time   `json:"deployedAt"`
}

type Environment struct {
	Class    string `json:"class"`
	Identity string `json:"identity,omitempty"`
}

type App struct {
	Name         string   `json:"name"`
	BuildID      string   `json:"buildId,omitempty"`
	DeploymentID string   `json:"deploymentId,omitempty"`
	URLs         []string `json:"urls"`
}

func Path(projectDir string) string {
	return filepath.Join(projectDir, scratchDirName, fileName)
}

func Write(projectDir string, r Result) error {
	r.SchemaVersion = SchemaVersion
	if r.DeployedAt.IsZero() {
		r.DeployedAt = time.Now().UTC()
	}
	if r.Apps == nil {
		r.Apps = []App{}
	}
	for at := range r.Apps {
		if r.Apps[at].URLs == nil {
			r.Apps[at].URLs = []string{}
		}
	}

	doc, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encode deploy result: %w", err)
	}
	doc = append(doc, '\n')

	path := Path(projectDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), fileName+".*")
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(doc); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func Clear(projectDir string) error {
	if err := os.Remove(Path(projectDir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", Path(projectDir), err)
	}
	return nil
}

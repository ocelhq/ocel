package deployrecord

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/project"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/statedir"
)

const fileName = "deploy-result.json"

type Record struct {
	Slug        string      `json:"slug"`
	Environment Environment `json:"environment"`
	Provider    Provider    `json:"provider"`
	PromotionID string      `json:"promotionId"`
	Tag         string      `json:"tag,omitempty"`
	Apps        []App       `json:"apps"`
	DeployedAt  time.Time   `json:"deployedAt"`
}

type Provider struct {
	Name string `json:"name"`
}

type Environment struct {
	Tier     string `json:"tier"`
	Identity string `json:"identity,omitempty"`
}

type App struct {
	Name         string   `json:"name"`
	BuildID      string   `json:"buildId,omitempty"`
	DeploymentID string   `json:"deploymentId,omitempty"`
	URLs         []string `json:"urls"`
}

func New(cfg *project.Project, manifest *contractv1.Manifest, env *environmentv1.Environment, tag, promotionID string, results []*progressv1.AppResult) (Record, error) {
	apps := make([]App, 0, len(manifest.GetApps()))
	for _, a := range manifest.GetApps() {
		buildID, err := build.BuildID(cfg.Dir, a.GetName())
		if err != nil {
			return Record{}, err
		}
		apps = append(apps, App{
			Name:         a.GetName(),
			BuildID:      buildID,
			DeploymentID: a.GetDeploymentId(),
			URLs:         appURLs(results, a.GetName()),
		})
	}
	return Record{
		Slug:        cfg.Slug,
		Environment: Environment{Tier: tierKey(env.GetTier()), Identity: env.GetIdentity()},
		Provider:    providerOf(cfg),
		PromotionID: promotionID,
		Tag:         tag,
		Apps:        apps,
	}, nil
}

func providerOf(cfg *project.Project) Provider {
	if cfg.Provider == nil {
		return Provider{}
	}
	return Provider{Name: cfg.Provider.ID}
}

func appURLs(results []*progressv1.AppResult, name string) []string {
	for _, result := range results {
		if result.GetApp() == name {
			return result.GetUrls()
		}
	}
	return nil
}

func tierKey(tier environmentv1.Tier) string {
	switch tier {
	case environmentv1.Tier_TIER_PRODUCTION:
		return "production"
	case environmentv1.Tier_TIER_PREVIEW:
		return "preview"
	default:
		return "unspecified"
	}
}

func Path(projectDir string) string {
	return filepath.Join(projectDir, statedir.Name, fileName)
}

func Write(projectDir string, r Record) error {
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

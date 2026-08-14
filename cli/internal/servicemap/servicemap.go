package servicemap

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
)

const SchemaVersion = 1

const scratchDirName = ".ocel"

const fileName = "service-map.json"

type Record struct {
	SchemaVersion int         `json:"schemaVersion"`
	Slug          string      `json:"slug"`
	Environment   Environment `json:"environment"`
	PromotionID   string      `json:"promotionId"`
	Tag           string      `json:"tag,omitempty"`
	Links         []Link      `json:"links"`
	Usages        []Usage     `json:"usages"`
	DeployedAt    time.Time   `json:"deployedAt"`
}

type Environment struct {
	Class    string `json:"class"`
	Identity string `json:"identity,omitempty"`
}

type Link struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	VarKeys []string `json:"varKeys"`
	Grants  []Grant  `json:"grants"`
}

type Grant struct {
	Verb    string   `json:"verb,omitempty"`
	Actions []string `json:"actions"`
}

type Usage struct {
	App      string   `json:"app"`
	Resource string   `json:"resource"`
	Files    []string `json:"files"`
}

type Deploy struct {
	Slug        string
	Environment Environment
	PromotionID string
	Tag         string
}

func Derive(d Deploy, manifest *deploymentsv1.Manifest, links []*linksv1.Link) Record {
	return Record{
		Slug:        d.Slug,
		Environment: d.Environment,
		PromotionID: d.PromotionID,
		Tag:         d.Tag,
		Links:       deriveLinks(links),
		Usages:      deriveUsages(manifest),
	}
}

func deriveLinks(links []*linksv1.Link) []Link {
	out := make([]Link, 0, len(links))
	for _, l := range links {
		keys := make([]string, 0, len(l.GetProperties()))
		for k := range l.GetProperties() {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		grants := make([]Grant, 0, len(l.GetGrants()))
		for _, g := range l.GetGrants() {
			grants = append(grants, Grant{Verb: g.GetLabel(), Actions: append([]string(nil), g.GetActions()...)})
		}

		out = append(out, Link{Name: l.GetName(), Type: l.GetType(), VarKeys: keys, Grants: grants})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func deriveUsages(manifest *deploymentsv1.Manifest) []Usage {
	out := make([]Usage, 0, len(manifest.GetUsages()))
	for _, u := range manifest.GetUsages() {
		out = append(out, Usage{
			App:      u.GetApp(),
			Resource: u.GetResource(),
			Files:    append([]string(nil), u.GetFiles()...),
		})
	}
	return out
}

func Path(projectDir string) string {
	return filepath.Join(projectDir, scratchDirName, fileName)
}

func Publish(projectDir string, r Record) error {
	r.SchemaVersion = SchemaVersion
	if r.DeployedAt.IsZero() {
		r.DeployedAt = time.Now().UTC()
	}
	if r.Links == nil {
		r.Links = []Link{}
	}
	if r.Usages == nil {
		r.Usages = []Usage{}
	}

	doc, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encode service map: %w", err)
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

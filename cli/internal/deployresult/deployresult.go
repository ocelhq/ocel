// Package deployresult writes the machine-readable record of a successful
// deploy to <projectDir>/.ocel/deploy-result.json.
//
// It exists because a deploy's outcome has to outlive the CLI process that
// produced it: anything scripting Ocel — a CI job that deploys in one step and
// asserts against the URL in the next, a lifecycle script the Next.js adapter
// test harness runs as a separate process — needs the URL, the promotion id
// and the build ids after `ocel deploy` has exited. The alternative is scraping
// the human-facing success screen, and that output is deliberately not a
// contract: it is free to change with the UI.
//
// The file is written only on success and only after the deploy is committed,
// and Clear removes any earlier run's file before a new one starts, so its
// presence always means "this project's last deploy attempt succeeded and this
// is what it produced" — never a stale result mistaken for the current one.
//
// The JSON shape is stable and versioned by SchemaVersion: fields are only ever
// added, and a consumer that reads an unknown SchemaVersion should refuse rather
// than guess.
package deployresult

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SchemaVersion is the version of the deploy-result document's shape.
const SchemaVersion = 1

// scratchDirName is the Ocel-internal folder next to the resolved config,
// shared with projectconfig/appbuilder/deployui.
const scratchDirName = ".ocel"

const fileName = "deploy-result.json"

// Result is the deploy-result document, populated from what the CLI knows once
// the provider's Deploy RPC reaches a successful terminal result.
type Result struct {
	SchemaVersion int         `json:"schemaVersion"`
	Slug          string      `json:"slug"`
	Environment   Environment `json:"environment"`
	// PromotionID identifies the promotion this deploy created, as reported on
	// the provider's terminal result.
	PromotionID string `json:"promotionId"`
	// Tag is the immutable label this deploy was stamped with, absent when it
	// was untagged.
	Tag string `json:"tag,omitempty"`
	// AppURLs are the user-facing URLs the success screen featured, in the
	// provider's priority order.
	AppURLs []string `json:"appUrls"`
	Apps    []App    `json:"apps"`
	// DeployedAt is when the deploy completed, stamped by Write when unset.
	DeployedAt time.Time `json:"deployedAt"`
}

// Environment identifies which substrate the deploy landed on: its class
// ("production"/"preview") and, for a preview, the environment it is keyed by.
type Environment struct {
	Class    string `json:"class"`
	Identity string `json:"identity,omitempty"`
}

// App pairs a deployed app with the build id this deploy made active for it.
// BuildID is absent for a framework whose build carries no build id of its own
// (the provider mints one the CLI never sees).
type App struct {
	Name    string `json:"name"`
	BuildID string `json:"buildId,omitempty"`
}

// Path is the result document's path for a project directory.
func Path(projectDir string) string {
	return filepath.Join(projectDir, scratchDirName, fileName)
}

// Write stamps the schema version and, when unset, the completion time, then
// replaces the project's result document. The write is atomic (temp file +
// rename) so a reader never observes a half-written document.
func Write(projectDir string, r Result) error {
	r.SchemaVersion = SchemaVersion
	if r.DeployedAt.IsZero() {
		r.DeployedAt = time.Now().UTC()
	}
	if r.AppURLs == nil {
		r.AppURLs = []string{}
	}
	if r.Apps == nil {
		r.Apps = []App{}
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

// Clear removes the project's result document, if any. Deploy paths call it
// before provisioning so a failed run cannot leave the previous run's result
// behind to be read as this one's.
func Clear(projectDir string) error {
	if err := os.Remove(Path(projectDir)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", Path(projectDir), err)
	}
	return nil
}

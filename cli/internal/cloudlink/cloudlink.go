// Package cloudlink stores a working tree's association with an Ocel Cloud
// project in <projectDir>/.ocel/link.json.
//
// Ocel layers as CLI -> SDK -> Ocel Cloud, with Cloud at the top and poppable.
// The link is the one place that association is recorded, and it lives in the
// gitignored scratch dir rather than in the tracked config: cloud identity is
// per-checkout, not per-repository, so two clones of the same project can point
// at different accounts, and a clone can point at no account at all.
//
// A record is scoped to the control plane that issued it. Read takes the API
// URL the current invocation is targeting and reports unlinked when the record
// was written against a different one — a link to another control plane is not
// a partial match to reconcile, it simply does not apply.
package cloudlink

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// scratchDirName is the Ocel-internal folder next to the project root, shared
// with projectconfig/deployresult/providerlocator.
const scratchDirName = ".ocel"

const fileName = "link.json"

// Link is the working tree's Ocel Cloud association. ProjectName is a cached
// display name so routine commands can name the project without a round trip.
type Link struct {
	APIURL         string `json:"apiUrl"`
	OrganizationID string `json:"organizationId"`
	ProjectID      string `json:"projectId"`
	ProjectName    string `json:"projectName"`
}

func path(projectDir string) string {
	return filepath.Join(projectDir, scratchDirName, fileName)
}

// Read returns the link projectDir holds for the control plane at apiURL, or
// nil when the directory is unlinked — no record at all, or one written against
// a different control plane.
//
// A record that exists but cannot be decoded is an error, not silence: it means
// something wrote the file that shouldn't have, and `ocel unlink` clears it.
func Read(projectDir, apiURL string) (*Link, error) {
	data, err := os.ReadFile(path(projectDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path(projectDir), err)
	}

	var link Link
	if err := json.Unmarshal(data, &link); err != nil {
		return nil, fmt.Errorf("read %s: %w (run `ocel unlink` to clear it)", path(projectDir), err)
	}

	if normalizeAPIURL(link.APIURL) != normalizeAPIURL(apiURL) {
		return nil, nil
	}
	return &link, nil
}

// Write replaces projectDir's link record. The write is atomic (temp file +
// rename) so a reader never observes a half-written record.
func Write(projectDir string, link Link) error {
	link.APIURL = normalizeAPIURL(link.APIURL)

	doc, err := json.MarshalIndent(link, "", "  ")
	if err != nil {
		return fmt.Errorf("encode link: %w", err)
	}
	doc = append(doc, '\n')

	dest := path(projectDir)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(dest), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), fileName+".*")
	if err != nil {
		return fmt.Errorf("create %s: %w", dest, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(doc); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", dest, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	return nil
}

// Clear removes projectDir's link record, whichever control plane it names, and
// reports whether there was one to remove.
func Clear(projectDir string) (bool, error) {
	if err := os.Remove(path(projectDir)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("remove %s: %w", path(projectDir), err)
	}
	return true, nil
}

func normalizeAPIURL(u string) string {
	return strings.TrimRight(strings.TrimSpace(u), "/")
}

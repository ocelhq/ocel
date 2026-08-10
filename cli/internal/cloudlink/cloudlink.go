package cloudlink

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const scratchDirName = ".ocel"

const fileName = "link.json"

type Link struct {
	APIURL         string `json:"apiUrl"`
	OrganizationID string `json:"organizationId"`
	ProjectID      string `json:"projectId"`
	ProjectName    string `json:"projectName"`
}

func path(projectDir string) string {
	return filepath.Join(projectDir, scratchDirName, fileName)
}

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

package consolebinding

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const scratchDirName = ".ocel"

const fileName = "console.json"

type Binding struct {
	APIURL         string `json:"apiUrl"`
	OrganizationID string `json:"organizationId"`
	ProjectID      string `json:"projectId"`
	ProjectName    string `json:"projectName"`
}

func path(projectDir string) string {
	return filepath.Join(projectDir, scratchDirName, fileName)
}

func Read(projectDir, apiURL string) (*Binding, error) {
	data, err := os.ReadFile(path(projectDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path(projectDir), err)
	}

	var binding Binding
	if err := json.Unmarshal(data, &binding); err != nil {
		return nil, fmt.Errorf("read %s: %w (run `ocel console unlink` to clear it)", path(projectDir), err)
	}

	if normalizeAPIURL(binding.APIURL) != normalizeAPIURL(apiURL) {
		return nil, nil
	}
	return &binding, nil
}

func Write(projectDir string, binding Binding) error {
	binding.APIURL = normalizeAPIURL(binding.APIURL)

	doc, err := json.MarshalIndent(binding, "", "  ")
	if err != nil {
		return fmt.Errorf("encode binding: %w", err)
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

package console

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/pkg/statedir"
)

const linkFileName = "console.json"

type Link struct {
	APIURL         string `json:"apiUrl"`
	OrganizationID string `json:"organizationId"`
	ProjectID      string `json:"projectId"`
	ProjectName    string `json:"projectName"`
}

func linkPath(projectDir string) string {
	return filepath.Join(projectDir, statedir.Name, linkFileName)
}

var ErrNotLinked = errors.New("this directory isn't linked to a console project")

func ReadLink(projectDir, apiURL string) (*Link, error) {
	data, err := os.ReadFile(linkPath(projectDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", linkPath(projectDir), err)
	}

	var link Link
	if err := json.Unmarshal(data, &link); err != nil {
		return nil, fmt.Errorf("read %s: %w (run `ocel unlink` to clear it)", linkPath(projectDir), err)
	}

	if normalizeAPIURL(link.APIURL) != normalizeAPIURL(apiURL) {
		return nil, nil
	}
	return &link, nil
}

func WriteLink(projectDir string, link Link) error {
	link.APIURL = normalizeAPIURL(link.APIURL)

	doc, err := json.MarshalIndent(link, "", "  ")
	if err != nil {
		return fmt.Errorf("encode link: %w", err)
	}
	doc = append(doc, '\n')

	dest := linkPath(projectDir)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(dest), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), linkFileName+".*")
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

func DeleteLink(projectDir string) (bool, error) {
	if err := os.Remove(linkPath(projectDir)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("remove %s: %w", linkPath(projectDir), err)
	}
	return true, nil
}

func normalizeAPIURL(u string) string {
	return strings.TrimRight(strings.TrimSpace(u), "/")
}

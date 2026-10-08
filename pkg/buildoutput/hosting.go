package buildoutput

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/pkg/edge"
)

const HostingFile = "hosting.json"

type NeedDetail struct {
	Count    int      `json:"count"`
	Routes   []string `json:"routes,omitempty"`
	Matchers []string `json:"matchers,omitempty"`
}

type Hosting struct {
	Framework        string                   `json:"framework"`
	FrameworkBuildID string                   `json:"frameworkBuildId"`
	EdgeRouting      bool                     `json:"edgeRouting"`
	Entry            string                   `json:"entry"`
	Needs            map[edge.Need]NeedDetail `json:"needs"`
}

func ReadHosting(root, app string) (Hosting, bool, error) {
	raw, err := os.ReadFile(filepath.Join(AppRoot(root, app), HostingFile))
	if errors.Is(err, fs.ErrNotExist) {
		return Hosting{}, false, nil
	}
	if err != nil {
		return Hosting{}, false, fmt.Errorf("read hosting.json of %s: %w", app, err)
	}
	var hosting Hosting
	if err := json.Unmarshal(raw, &hosting); err != nil {
		return Hosting{}, false, fmt.Errorf("parse hosting.json of %s: %w", app, err)
	}
	return hosting, true, nil
}

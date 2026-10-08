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

const HostingVersion = 1

type NeedDetail struct {
	Count    int      `json:"count"`
	Routes   []string `json:"routes,omitempty"`
	Matchers []string `json:"matchers,omitempty"`
}

type Hosting struct {
	Version          int                      `json:"version"`
	Framework        string                   `json:"framework"`
	FrameworkBuildID string                   `json:"frameworkBuildId"`
	RootFunction     string                   `json:"rootFunction"`
	RouteTable       edge.RouteTableFormat    `json:"routeTable,omitempty"`
	Static           *edge.Static             `json:"static,omitempty"`
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
	if hosting.Version != HostingVersion {
		return Hosting{}, false, fmt.Errorf("the hosting.json of %s is version %d, and this CLI reads version %d; build the app with an adapter and CLI of the same release", app, hosting.Version, HostingVersion)
	}
	return hosting, true, nil
}

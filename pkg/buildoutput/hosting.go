package buildoutput

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
)

const HostingFile = "hosting.json"

const HostingVersion = 1

type RouteTableFormat string

const RouteTableNext RouteTableFormat = "next"

type NeedDetail struct {
	Count    int      `json:"count"`
	Routes   []string `json:"routes,omitempty"`
	Matchers []string `json:"matchers,omitempty"`
}

type Static struct {
	ImmutablePrefixes      []string `json:"immutablePrefixes"`
	MustRevalidatePrefixes []string `json:"mustRevalidatePrefixes,omitempty"`
}

type Hosting struct {
	Version          int                      `json:"version"`
	Framework        string                   `json:"framework"`
	FrameworkBuildID string                   `json:"frameworkBuildId"`
	RootFunction     string                   `json:"rootFunction"`
	RouteTable       RouteTableFormat         `json:"routeTable,omitempty"`
	Static           *Static                  `json:"static,omitempty"`
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

func (s *Static) IsImmutable(path string) bool {
	if s == nil {
		return false
	}
	under := func(prefix string) bool { return strings.HasPrefix(path, prefix) }
	return slices.ContainsFunc(s.ImmutablePrefixes, under) && !slices.ContainsFunc(s.MustRevalidatePrefixes, under)
}

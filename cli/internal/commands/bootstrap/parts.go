package bootstrap

import (
	"slices"
	"strings"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func parseRemoveFlag(raw string) []string {
	var named []string
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name != "" && !slices.Contains(named, name) {
			named = append(named, name)
		}
	}
	return named
}

func isInstalled(part *contractv1.BootstrapPart) bool {
	switch part.GetState() {
	case contractv1.BootstrapPartState_BOOTSTRAP_PART_STATE_READY, contractv1.BootstrapPartState_BOOTSTRAP_PART_STATE_CREATING:
		return true
	}
	return false
}

func installedOf(parts []*contractv1.BootstrapPart, named []string) []string {
	var installed []string
	for _, name := range named {
		if slices.ContainsFunc(parts, func(part *contractv1.BootstrapPart) bool { return part.GetName() == name && isInstalled(part) }) {
			installed = append(installed, name)
		}
	}
	return installed
}

func usersOf(parts []*contractv1.BootstrapPart, names []string) []string {
	var users []string
	for _, part := range parts {
		if !slices.Contains(names, part.GetName()) {
			continue
		}
		for _, project := range part.GetUsedBy() {
			if !slices.Contains(users, project) {
				users = append(users, project)
			}
		}
	}
	slices.Sort(users)
	return users
}

func without(names, taken []string) []string {
	var kept []string
	for _, name := range names {
		if !slices.Contains(taken, name) {
			kept = append(kept, name)
		}
	}
	return kept
}

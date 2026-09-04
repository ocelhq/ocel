package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Manager string

const (
	Unknown     Manager = ""
	Pnpm        Manager = "pnpm"
	Npm         Manager = "npm"
	YarnClassic Manager = "yarn"
	YarnBerry   Manager = "yarn-berry"
	Bun         Manager = "bun"
)

const (
	pnpmLock  = "pnpm-lock.yaml"
	npmLock   = "package-lock.json"
	yarnLock  = "yarn.lock"
	bunLock   = "bun.lock"
	yarnRcYml = ".yarnrc.yml"

	berryLockHeader = "__metadata:"
)

type Commands struct {
	Install string
	Build   string
	Start   string
}

type behaviour struct {
	declaredAs string
	lockfiles  []string
	runner     string
	runtime    string
	install    func(Location) string
	build      func(Location) string
	start      func(Location) string
	replaces   []string
}

var behaviours = map[Manager]behaviour{
	Pnpm: {
		declaredAs: "pnpm",
		lockfiles:  []string{pnpmLock},
		runner:     "pnpm",
		runtime:    "node",
		install: func(l Location) string {
			return fmt.Sprintf("pnpm install --frozen-lockfile --filter ./%s...", l.Path)
		},
		build:    func(l Location) string { return fmt.Sprintf("pnpm --filter ./%s... run build", l.Path) },
		start:    func(l Location) string { return fmt.Sprintf("pnpm --filter ./%s run start", l.Path) },
		replaces: []string{"pnpm install --frozen-lockfile --prefer-offline", "pnpm install"},
	},
	Npm: {
		declaredAs: "npm",
		lockfiles:  []string{npmLock, "npm-shrinkwrap.json"},
		runner:     "npm",
		runtime:    "node",
		build:      func(l Location) string { return fmt.Sprintf("npm run build -w %s", l.Path) },
		start:      func(l Location) string { return fmt.Sprintf("npm run start -w %s", l.Path) },
	},
	YarnBerry: {
		runner:  "yarn",
		runtime: "node",
		install: byName(func(l Location) string { return fmt.Sprintf("yarn workspaces focus %s", l.App.Name) }),
		build: byName(func(l Location) string {
			return fmt.Sprintf("yarn workspaces foreach -R -t --from %s run build", l.App.Name)
		}),
		start:    byName(func(l Location) string { return fmt.Sprintf("yarn workspace %s run start", l.App.Name) }),
		replaces: []string{"yarn install --check-cache"},
	},
	YarnClassic: {
		declaredAs: "yarn",
		lockfiles:  []string{yarnLock},
		runner:     "yarn",
		runtime:    "node",
		build:      byName(func(l Location) string { return fmt.Sprintf("yarn workspace %s run build", l.App.Name) }),
		start:      byName(func(l Location) string { return fmt.Sprintf("yarn workspace %s run start", l.App.Name) }),
	},
	Bun: {
		declaredAs: "bun",
		lockfiles:  []string{bunLock, "bun.lockb"},
		runner:     "bun",
		runtime:    "bun",
		build:      byName(func(l Location) string { return fmt.Sprintf("bun run --filter %s build", l.App.Name) }),
		start:      func(l Location) string { return l.inAppDir("bun run start") },
	},
}

func byName(command func(Location) string) func(Location) string {
	return func(l Location) string {
		if l.App.Name == "" {
			return ""
		}
		return command(l)
	}
}

func detect(root string) Manager {
	declared := declaredManager(root)
	present := map[Manager]bool{}
	for manager, m := range behaviours {
		for _, lock := range m.lockfiles {
			if _, err := os.Stat(filepath.Join(root, lock)); err != nil {
				continue
			}
			if manager == YarnClassic {
				manager = yarnAt(root, declared)
			}
			present[manager] = true
		}
	}
	if len(present) == 0 {
		return Unknown
	}
	if len(present) == 1 {
		for manager := range present {
			return manager
		}
	}
	if declared != Unknown {
		return declared
	}
	for _, manager := range []Manager{Pnpm, YarnBerry, YarnClassic, Bun, Npm} {
		if present[manager] {
			return manager
		}
	}
	return Unknown
}

func yarnAt(root string, declared Manager) Manager {
	if _, err := os.Stat(filepath.Join(root, yarnRcYml)); err == nil {
		return YarnBerry
	}
	if declared == YarnBerry {
		return YarnBerry
	}
	if read, err := os.ReadFile(filepath.Join(root, yarnLock)); err == nil && strings.Contains(string(read), berryLockHeader) {
		return YarnBerry
	}
	return YarnClassic
}

func declaredManager(root string) Manager {
	m, err := readManifest(filepath.Join(root, manifestName))
	if err != nil {
		return Unknown
	}
	name, version, _ := strings.Cut(m.PackageManager, "@")
	name = strings.TrimSpace(name)
	if name == behaviours[YarnClassic].declaredAs {
		return yarnGeneration(version)
	}
	for manager, m := range behaviours {
		if m.declaredAs != "" && m.declaredAs == name {
			return manager
		}
	}
	return Unknown
}

func yarnGeneration(version string) Manager {
	major, _, _ := strings.Cut(strings.TrimSpace(version), ".")
	if generation, err := strconv.Atoi(major); err == nil && generation < 2 {
		return YarnClassic
	}
	return YarnBerry
}

func (l Location) Commands() (Commands, error) {
	var commands Commands
	if l.InWorkspace() {
		start := l.start()
		if start == "" {
			return Commands{}, fmt.Errorf(
				"app %q declares no start script, no main and no entry file beside its %s, and it is a member of the workspace at %s: the image would be left starting the workspace root's own start script, which serves something else — give %s a %q script",
				l.name(), manifestName, l.Root, filepath.ToSlash(filepath.Join(l.Path, manifestName)), "start",
			)
		}
		commands = Commands{Install: l.install(), Build: l.build(), Start: start}
	}
	if l.BuildCommand != "" {
		commands.Build = l.BuildCommand
	}
	return commands, nil
}

func (l Location) name() string {
	if l.App.Name != "" {
		return l.App.Name
	}
	return filepath.Base(l.Path)
}

func (l Location) install() string {
	if install := behaviours[l.Manager].install; install != nil {
		return install(l)
	}
	return ""
}

func (l Location) build() string {
	if !l.App.Build {
		return ""
	}
	if build := behaviours[l.Manager].build; build != nil {
		if scoped := build(l); scoped != "" {
			return scoped
		}
	}
	return l.inAppDir(l.runner() + " run build")
}

func (l Location) start() string {
	if !l.App.Start {
		return l.startsItself()
	}
	if start := behaviours[l.Manager].start; start != nil {
		if scoped := start(l); scoped != "" {
			return scoped
		}
	}
	return l.inAppDir(l.runner() + " run start")
}

func (l Location) startsItself() string {
	entry := l.App.Main
	if entry == "" {
		entry = l.App.Index
	}
	if entry == "" {
		return ""
	}
	return l.inAppDir(l.runtime() + " " + entry)
}

func (l Location) inAppDir(command string) string {
	return fmt.Sprintf("cd %s && %s", l.Path, command)
}

func (l Location) runner() string {
	if runner := behaviours[l.Manager].runner; runner != "" {
		return runner
	}
	return "npm"
}

func (l Location) runtime() string {
	if runtime := behaviours[l.Manager].runtime; runtime != "" {
		return runtime
	}
	return "node"
}

func ReplaceableInstalls(manager Manager) []string {
	return behaviours[manager].replaces
}

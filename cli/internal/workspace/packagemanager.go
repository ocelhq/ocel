package workspace

import (
	"fmt"
	"os"
	"path/filepath"
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
)

type Commands struct {
	Install string
	Build   string
	Start   string
}

func detect(root string) Manager {
	present := map[Manager]bool{}
	for name, manager := range map[string]Manager{
		pnpmLock:              Pnpm,
		npmLock:               Npm,
		"npm-shrinkwrap.json": Npm,
		yarnLock:              yarnAt(root),
		bunLock:               Bun,
		"bun.lockb":           Bun,
	} {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
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
	if named := declaredManager(root); named != Unknown {
		return named
	}
	for _, manager := range []Manager{Pnpm, YarnBerry, YarnClassic, Bun, Npm} {
		if present[manager] {
			return manager
		}
	}
	return Unknown
}

func yarnAt(root string) Manager {
	if _, err := os.Stat(filepath.Join(root, yarnRcYml)); err == nil {
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
	switch strings.TrimSpace(name) {
	case "pnpm":
		return Pnpm
	case "npm":
		return Npm
	case "bun":
		return Bun
	case "yarn":
		if strings.HasPrefix(version, "1.") {
			return YarnClassic
		}
		return YarnBerry
	}
	return Unknown
}

func (l Location) Commands() Commands {
	var commands Commands
	if l.InWorkspace() {
		commands = Commands{Install: l.install(), Build: l.build(), Start: l.start()}
	}
	if l.BuildCommand != "" {
		commands.Build = l.BuildCommand
	}
	return commands
}

func (l Location) install() string {
	switch l.Manager {
	case Pnpm:
		return fmt.Sprintf("pnpm install --frozen-lockfile --prefer-offline --filter ./%s...", l.Path)
	case YarnBerry:
		if l.App.Name == "" {
			return ""
		}
		return fmt.Sprintf("yarn workspaces focus %s", l.App.Name)
	default:
		return ""
	}
}

func (l Location) build() string {
	if !l.App.Build {
		return ""
	}
	switch l.Manager {
	case Pnpm:
		return fmt.Sprintf("pnpm --filter ./%s... run build", l.Path)
	case YarnBerry:
		if l.App.Name == "" {
			break
		}
		return fmt.Sprintf("yarn workspaces foreach -R -t --from %s run build", l.App.Name)
	case YarnClassic:
		if l.App.Name == "" {
			break
		}
		return fmt.Sprintf("yarn workspace %s run build", l.App.Name)
	case Npm:
		return fmt.Sprintf("npm run build -w %s", l.Path)
	case Bun:
		if l.App.Name == "" {
			break
		}
		return fmt.Sprintf("bun run --filter %s build", l.App.Name)
	}
	return l.inAppDir(l.runner() + " run build")
}

func (l Location) start() string {
	if !l.App.Start {
		return l.startsItself()
	}
	switch l.Manager {
	case Pnpm:
		return fmt.Sprintf("pnpm --filter ./%s run start", l.Path)
	case YarnBerry, YarnClassic:
		if l.App.Name == "" {
			break
		}
		return fmt.Sprintf("yarn workspace %s run start", l.App.Name)
	case Npm:
		return fmt.Sprintf("npm run start -w %s", l.Path)
	case Bun:
		return l.inAppDir("bun run start")
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
	runtime := "node"
	if l.Manager == Bun {
		runtime = "bun"
	}
	return l.inAppDir(runtime + " " + entry)
}

func (l Location) inAppDir(command string) string {
	return fmt.Sprintf("cd %s && %s", l.Path, command)
}

func (l Location) runner() string {
	switch l.Manager {
	case Pnpm:
		return "pnpm"
	case Bun:
		return "bun"
	case YarnBerry, YarnClassic:
		return "yarn"
	default:
		return "npm"
	}
}

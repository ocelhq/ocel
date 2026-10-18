package host

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
)

const (
	dockerShims   = boxapi.LibDir + "/bin"
	DockerShim    = dockerShims + "/docker"
	LoginShell    = boxapi.LibDir + "/login"
	sudoersDocker = sudoersRoot + "/ocel-docker"

	deployPath = dockerShims + ":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)

func dockerShim() []byte {
	return []byte("#!/bin/sh\nexec sudo -n " + LiveBinary + " " + boxapi.DockerCommand + " \"$@\"\n")
}

func loginShell() []byte {
	return []byte("#!/bin/sh\nPATH=" + deployPath + "\nexport PATH\nexec /bin/sh \"$@\"\n")
}

func dockerSudoers() []byte {
	return []byte(deployUser + " ALL=(root) NOPASSWD: " + LiveBinary + " " + boxapi.DockerCommand + " *\n")
}

func dockerGrants(front Front) boxapi.DockerGrants {
	grants := boxapi.DockerGrants{
		Binds: []boxapi.DockerBind{
			{Path: SwitchboardDir},
			{Path: RealtimeDir},
			{Path: LiveSocketDir},
			{Path: boxapi.RoutingDir},
			{Path: ConnectorRun},
			{Path: boxapi.SwitchboardControlDir, Writable: true},
			{Path: boxapi.FrontDir, Writable: true},
			{Path: TunnelDir, Writable: true},
		},
		Containers: []string{SwitchboardContainer, caddy.Container, TunnelContainer},
		Networks:   []string{},
	}
	if placed := destination(openFront(front, frontBox{})); placed != "" {
		grants.Binds = append(grants.Binds, boxapi.DockerBind{Path: filepath.Dir(placed), Writable: true})
	}
	for _, joined := range front.joined() {
		grants.Networks = append(grants.Networks, joined.name)
	}
	if front.Caddy != nil && front.Caddy.Container != "" {
		grants.Containers = append(grants.Containers, front.Caddy.Container)
	}
	slices.SortFunc(grants.Binds, func(a, b boxapi.DockerBind) int { return strings.Compare(a.Path, b.Path) })
	return grants
}

func dockerGrantsContent(front Front) []byte {
	written, err := json.MarshalIndent(dockerGrants(front), "", "  ")
	if err != nil {
		panic(err)
	}
	return append(written, '\n')
}

func DockerAccessItems(front Front) []Item {
	return []Item{
		dir(dockerShims, 0o755, rootOwner, ""),
		{Kind: KindFile, Name: DockerShim, Mode: 0o755, Owner: rootOwner, Content: dockerShim(), Note: "docker for the deploy login, through the checks in " + LiveBinary},
		{Kind: KindFile, Name: LoginShell, Mode: 0o755, Owner: rootOwner, Content: loginShell(), Note: "the deploy login's shell"},
		dir(boxapi.DockerConfigDir, 0o700, rootOwner, "registry logins, one per pull"),
		{Kind: KindFile, Name: boxapi.DockerGrantsPath, Mode: 0o644, Owner: rootOwner, Content: dockerGrantsContent(front), Note: "what deploys may mount, join and run inside"},
		{Kind: KindFile, Name: sudoersDocker, Mode: 0o440, Owner: rootOwner, Content: dockerSudoers(), Note: "sudo for docker, checked"},
	}
}

func dockerAccessRemovals() []removal {
	return []removal{
		taking(KindFile, sudoersDocker, ""),
		taking(KindFile, boxapi.DockerGrantsPath, ""),
		taking(KindDir, boxapi.DockerConfigDir, ""),
		taking(KindFile, LoginShell, ""),
		taking(KindFile, DockerShim, ""),
		taking(KindDir, dockerShims, ""),
	}
}

package host

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/tunnel"
)

const (
	dockerShims   = boxapi.LibDir + "/bin"
	DockerShim    = dockerShims + "/docker"
	LoginShell    = boxapi.LibDir + "/login"
	sudoersDocker = machine.SudoersRoot + "/ocel-docker"

	deployPath = dockerShims + ":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)

func dockerShim() []byte {
	checked := "exec sudo -n " + LiveBinary + " " + boxapi.DockerCommand
	return []byte("#!/bin/sh\n" +
		"if [ -n \"$" + boxapi.TraceParentEnvVar + "\" ] && [ -n \"$" + boxapi.TraceSocketEnvVar + "\" ]; then\n" +
		"  " + checked + " \"--" + boxapi.TraceParentFlag + "=$" + boxapi.TraceParentEnvVar + "\" \"--" + boxapi.TraceSocketFlag + "=$" + boxapi.TraceSocketEnvVar + "\" \"$@\"\n" +
		"fi\n" +
		checked + " \"$@\"\n")
}

func loginShell() []byte {
	return []byte("#!/bin/sh\nPATH=" + deployPath + "\nexport PATH\nexec /bin/sh \"$@\"\n")
}

func dockerSudoers() []byte {
	return []byte(deployUser + " ALL=(root) NOPASSWD: " + LiveBinary + " " + boxapi.DockerCommand + " *\n")
}

func dockerGrants(fronting front.Proxy) boxapi.DockerGrants {
	grants := boxapi.DockerGrants{
		Binds: []boxapi.DockerBind{
			{Path: boxapi.SwitchboardDir},
			{Path: RealtimeDir},
			{Path: boxapi.SocketDir},
			{Path: boxapi.RoutingDir},
			{Path: ConnectorRun},
			{Path: boxapi.SwitchboardControlDir, Writable: true},
			{Path: boxapi.FrontDir, Writable: true},
			{Path: tunnel.Dir, Writable: true},
		},
		Containers: []string{boxapi.Switchboard, caddy.Container, tunnel.ContainerName},
		Commands: map[string][][]string{
			caddy.Container: {caddy.ReloadCommand(), caddy.ListenersCommand(), frontProxy().Ready, frontProxy().MountInodesCommand()},
		},
		Networks: []string{},
	}
	if placed := routing.Destination(fronting.Front().Open(frontBox{})); placed != "" {
		grants.Binds = append(grants.Binds, boxapi.DockerBind{Path: filepath.Dir(placed), Writable: true})
	}
	for _, joined := range fronting.Front().Joined() {
		grants.Networks = append(grants.Networks, joined.Name)
	}
	grants.Containers = append(grants.Containers, fronting.Front().GrantedContainers()...)
	slices.SortFunc(grants.Binds, func(a, b boxapi.DockerBind) int { return strings.Compare(a.Path, b.Path) })
	return grants
}

func dockerGrantsContent(fronting front.Proxy) []byte {
	written, err := json.MarshalIndent(dockerGrants(fronting), "", "  ")
	if err != nil {
		panic(err)
	}
	return append(written, '\n')
}

func DockerAccessItems(fronting front.Proxy) []machine.Item {
	return []machine.Item{
		machine.Dir(dockerShims, 0o755, machine.RootOwner, ""),
		{Kind: machine.KindFile, Name: DockerShim, Mode: 0o755, Owner: machine.RootOwner, Content: dockerShim(), Note: "docker for the deploy login, through the checks in " + LiveBinary},
		{Kind: machine.KindFile, Name: LoginShell, Mode: 0o755, Owner: machine.RootOwner, Content: loginShell(), Note: "the deploy login's shell"},
		machine.Dir(boxapi.DockerConfigDir, 0o700, machine.RootOwner, "registry logins, one per pull"),
		{Kind: machine.KindFile, Name: boxapi.DockerGrantsPath, Mode: 0o644, Owner: machine.RootOwner, Content: dockerGrantsContent(fronting), Note: "what deploys may mount, join and run inside"},
		{Kind: machine.KindFile, Name: sudoersDocker, Mode: 0o440, Owner: machine.RootOwner, Content: dockerSudoers(), Note: "sudo for docker, checked"},
	}
}

func dockerAccessRemovals() []machine.Removal {
	return []machine.Removal{
		machine.Taking(machine.KindFile, sudoersDocker, ""),
		machine.Taking(machine.KindFile, boxapi.DockerGrantsPath, ""),
		machine.Taking(machine.KindDir, boxapi.DockerConfigDir, ""),
		machine.Taking(machine.KindFile, LoginShell, ""),
		machine.Taking(machine.KindFile, DockerShim, ""),
		machine.Taking(machine.KindDir, dockerShims, ""),
	}
}

package routing

import (
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

const (
	SwitchboardPort     = "8080"
	SwitchboardUpstream = "unix/" + boxapi.FrontSocket
)

var SwitchboardPermission = proxy.Permission{Dial: "unix/" + boxapi.AdmitSocket, Path: boxapi.AdmitPath}

var SwitchboardCapabilities = []string{"DAC_OVERRIDE"}

func Placement(dir string, argv ...string) []string { return placementRun(false, dir, argv) }

func PlacementFed(dir string, argv ...string) []string { return placementRun(true, dir, argv) }

func placementRun(fed bool, dir string, argv []string) []string {
	run := []string{"docker", "run"}
	if fed {
		run = append(run, "-i")
	}
	run = append(run, "--rm", "--network", "none", "--read-only")
	run = append(run, docker.Confined(SwitchboardCapabilities, false)...)
	run = append(run,
		"--mount", docker.BoundMount(boxapi.SwitchboardDir, boxapi.SwitchboardMount, "readonly"),
		"--mount", docker.BoundMount(dir, dir),
		"--env", boxapi.PlaceEnv+"="+dir,
		docker.StaticImage, boxapi.SwitchboardMounted)
	return append(run, argv...)
}

func SwitchboardCommand(argv ...string) []string {
	return append([]string{"docker", "exec", boxapi.Switchboard, boxapi.SwitchboardMounted}, argv...)
}

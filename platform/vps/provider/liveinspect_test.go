package vps_test

import (
	"errors"
	"testing"
)

func TestAnObjectTheDaemonDoesNotHoldIsToldApartFromACommandItRefused(t *testing.T) {
	t.Parallel()

	for said, gone := range map[string]bool{
		"exit status 1: Error response from daemon: No such container: ocel-proxy":                                                                          true,
		"exit status 1: Error: No such object: ocel-proxy":                                                                                                  true,
		"exit status 1: Error response from daemon: No such image: ocel/live/live-pull:one":                                                                 true,
		"exit status 1: Error response from daemon: network ocel not found":                                                                                 true,
		"exit status 1: Error response from daemon: get ocel-proxy-data: no such volume":                                                                    true,
		"exit status 64: template parsing error: template: :1: malformed character constant: 'ocel.app'":                                                    false,
		"exit status 1: Cannot connect to the Docker daemon at unix:///var/run/docker.sock":                                                                 false,
		"exit status 127: bash: line 1: docker: command not found":                                                                                          false,
		"exit status 1: sudo: docker: command not found":                                                                                                    false,
		`exit status 126: docker: Error response from daemon: failed to create task for container: exec: "sh": executable file not found in $PATH: unknown`: false,
		"exit status 255: ssh: Could not resolve hostname box: Name or service not known":                                                                   false,
		"exit status 2: stat: cannot statx '/var/lib/ocel/proxy/helper': No such file or directory":                                                         false,
		"exit status 255": false,
	} {
		if absent(errors.New(said)) != gone {
			t.Errorf("the suite reads %q as absent=%v, and a command the daemon refused that reads as an absence turns every assertion built on it into a silent pass", said, !gone)
		}
	}
}

func TestWhatACommandInAContainerSaidIsToldApartFromTheEngineRefusingToRunIt(t *testing.T) {
	t.Parallel()

	for rendered, want := range map[string]string{
		"held\n" + containerSaid:                        "held",
		containerSaid:                                   "",
		"600:root\n" + containerSaid:                    "600:root",
		"one\ntwo\n" + containerSaid:                    "one\ntwo",
		"tcp 0 0 0.0.0.0:2019 LISTEN\n" + containerSaid: "tcp 0 0 0.0.0.0:2019 LISTEN",
	} {
		said, ran := spoken(rendered)
		if !ran || said != want {
			t.Errorf("spoken(%q) = %q, %v, want %q, true", rendered, said, ran, want)
		}
	}

	for _, refused := range []string{
		"",
		"Error response from daemon: No such container: ocel-proxy\n",
		"docker: Error response from daemon: network ocel not found.\n",
		"Cannot connect to the Docker daemon at unix:///var/run/docker.sock\n",
		"held\n",
	} {
		if said, ran := spoken(refused); ran {
			t.Errorf("spoken(%q) = %q, true: a command the engine never ran that reads as an answer is the value every assertion built on it then compares", refused, said)
		}
	}
}

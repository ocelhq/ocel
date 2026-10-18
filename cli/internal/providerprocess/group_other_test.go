//go:build !unix

package providerprocess

import "os/exec"

func leaveTheProvidersGroup(*exec.Cmd) {}

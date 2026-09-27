package payloads

import (
	"bytes"
	"debug/elf"
	"strings"
	"testing"
)

func TestTheNodeRuntimeShipsAsOneBundle(t *testing.T) {
	body := NodeRuntime()
	if len(body) == 0 {
		t.Fatal("NodeRuntime() has no bytes, and a node function boots through what it contains")
	}
	for _, relative := range []string{`from "./`, `from "../`} {
		if strings.Contains(string(body), relative) {
			t.Errorf("NodeRuntime() reads %s, and the image contains this file alone", relative)
		}
	}
}

func TestTheContainerRuntimeIsAStaticLinuxBinaryForTheOneArchitectureCloudRunRuns(t *testing.T) {
	body, err := ContainerRuntime(ContainerArch)
	if err != nil {
		t.Fatalf("ContainerRuntime(%s) = %v", ContainerArch, err)
	}
	binary, err := elf.NewFile(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("ContainerRuntime(%s) is no ELF binary: %v", ContainerArch, err)
	}
	if binary.Machine != elf.EM_X86_64 {
		t.Errorf("ContainerRuntime(%s) is built for %s, and Cloud Run runs x86_64 alone", ContainerArch, binary.Machine)
	}
	if section := binary.Section(".interp"); section != nil {
		t.Error("the container runtime asks for a dynamic loader, and it is appended to images that may have none")
	}
	if _, err := ContainerRuntime("arm64"); err == nil {
		t.Error("ContainerRuntime(arm64) handed something back, and an arm64 image would then be wrapped for a platform Cloud Run does not run")
	}
}

func TestTheEnvSourceSyncIsAStaticLinuxBinaryForTheOneArchitectureCloudRunRuns(t *testing.T) {
	binary, err := elf.NewFile(bytes.NewReader(EnvSourceSync()))
	if err != nil {
		t.Fatalf("EnvSourceSync() is no ELF binary: %v", err)
	}
	if binary.Machine != elf.EM_X86_64 {
		t.Errorf("EnvSourceSync() is built for %s, and Cloud Run runs x86_64 alone", binary.Machine)
	}
	if section := binary.Section(".interp"); section != nil {
		t.Error("the env source sync asks for a dynamic loader, and the image it runs in has none")
	}
	if bytes.Equal(EnvSourceSync(), containerRuntime) {
		t.Error("EnvSourceSync() is the container runtime")
	}
}

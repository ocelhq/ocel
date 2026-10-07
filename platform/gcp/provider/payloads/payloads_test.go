package payloads

import (
	"bytes"
	"debug/elf"
	"io/fs"
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

func TestTheNextRuntimeShipsItsEntrypointAndEveryCacheHandler(t *testing.T) {
	for _, name := range []string{"entrypoint.mjs", "cache-handler.cjs", "use-cache-default.cjs", "use-cache-remote.cjs"} {
		body, err := fs.ReadFile(NextRuntime(), name)
		if err != nil {
			t.Errorf("NextRuntime() holds no %s: %v", name, err)
			continue
		}
		if len(body) == 0 {
			t.Errorf("NextRuntime()'s %s is empty", name)
		}
	}
}

func TestTheNextServerRuntimeHoldsTheAdapterAndEveryCacheHandlerItNames(t *testing.T) {
	files := NextServerRuntime()
	for _, name := range []string{"server-adapter.mjs", "cache-handler.cjs", "use-cache-default.cjs", "use-cache-remote.cjs"} {
		if len(files[name]) == 0 {
			t.Errorf("NextServerRuntime() holds no bytes for %s, and next start loads it from the image", name)
		}
	}
	if len(files) != 4 {
		t.Errorf("NextServerRuntime() holds %d files, want the adapter and the three cache handlers", len(files))
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

func TestTheRealtimeGatewayIsAStaticLinuxBinaryForTheOneArchitectureCloudRunRuns(t *testing.T) {
	binary, err := elf.NewFile(bytes.NewReader(RealtimeGateway()))
	if err != nil {
		t.Fatalf("RealtimeGateway() is no ELF binary: %v", err)
	}
	if binary.Machine != elf.EM_X86_64 {
		t.Errorf("RealtimeGateway() is built for %s, and Cloud Run runs x86_64 alone", binary.Machine)
	}
	if section := binary.Section(".interp"); section != nil {
		t.Error("the realtime gateway asks for a dynamic loader, and the image it runs in has none")
	}
	if bytes.Equal(RealtimeGateway(), EnvSourceSync()) || bytes.Equal(RealtimeGateway(), containerRuntime) {
		t.Error("RealtimeGateway() is another payload")
	}
}

func TestTheBastionIsAStaticLinuxBinaryForTheOneArchitectureCloudRunRuns(t *testing.T) {
	binary, err := elf.NewFile(bytes.NewReader(Bastion()))
	if err != nil {
		t.Fatalf("Bastion() is no ELF binary: %v", err)
	}
	if binary.Machine != elf.EM_X86_64 {
		t.Errorf("Bastion() is built for %s, and Cloud Run runs x86_64 alone", binary.Machine)
	}
	if section := binary.Section(".interp"); section != nil {
		t.Error("the bastion asks for a dynamic loader, and the image it runs in has none")
	}
	if bytes.Equal(Bastion(), RealtimeGateway()) || bytes.Equal(Bastion(), EnvSourceSync()) || bytes.Equal(Bastion(), containerRuntime) {
		t.Error("Bastion() is another payload")
	}
}

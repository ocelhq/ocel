package payloads

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/platform/gcp/gateplugin"
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

func TestTheNextRuntimeShipsItsEntrypointAndSharpButNotTheServerPreload(t *testing.T) {
	files := NextRuntime()
	for _, name := range []string{"entrypoint.mjs", "node_modules/sharp/package.json"} {
		if len(files[name]) == 0 {
			t.Errorf("NextRuntime() holds no bytes for %s", name)
		}
	}
	if _, shipped := files[containerimage.NextServerPreloadFile]; shipped {
		t.Errorf("NextRuntime() holds %s, which only a Next container loads", containerimage.NextServerPreloadFile)
	}
	for name := range files {
		if strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
			t.Errorf("NextRuntime() names %q, want a path inside the runtime directory", name)
		}
	}
}

func TestTheNextServerRuntimeHoldsThePreloadAlone(t *testing.T) {
	files := NextServerRuntime()
	if len(files[containerimage.NextServerPreloadFile]) == 0 {
		t.Errorf("NextServerRuntime() holds no bytes for %s, and a Next container runs it from the image", containerimage.NextServerPreloadFile)
	}
	if len(files) != 1 {
		t.Errorf("NextServerRuntime() holds %d files, want the preload alone: the cache handlers are bundled into the entrypoint and the preload", len(files))
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

const minimumConfigRoomBytes = 1 << 20

func TestTheGatePluginIsAWasmModuleWithRoomLeftForItsConfig(t *testing.T) {
	body := GatePlugin()
	if !bytes.HasPrefix(body, []byte("\x00asm")) {
		t.Fatal("GatePlugin() is no WebAssembly module")
	}
	if room := gateplugin.PackageLimitBytes - len(body); room < minimumConfigRoomBytes {
		t.Errorf("GatePlugin() leaves %d bytes of the %d a plugin and its config may take together, want at least %d for the gates it holds",
			room, gateplugin.PackageLimitBytes, minimumConfigRoomBytes)
	}
}

func TestTheGatePluginIsAReactorExportingTheProxyWasmABI(t *testing.T) {
	exports, err := readWasmExports(GatePlugin())
	if err != nil {
		t.Fatalf("GatePlugin() exports cannot be read: %v", err)
	}
	if exports["_start"] {
		t.Error("GatePlugin() exports _start: a command module runs main and exits, and the load balancer refuses to load it")
	}
	for _, name := range []string{"_initialize", "proxy_abi_version_0_2_0", "proxy_on_context_create", "proxy_on_vm_start", "proxy_on_configure", "proxy_on_request_headers"} {
		if !exports[name] {
			t.Errorf("GatePlugin() does not export %s, which the load balancer calls to run a proxy-wasm plugin", name)
		}
	}
}

const wasmExportSection = 7

func readWasmExports(module []byte) (map[string]bool, error) {
	if !bytes.HasPrefix(module, []byte("\x00asm")) || len(module) < 8 {
		return nil, errors.New("no WebAssembly module header")
	}
	rest := module[8:]
	for len(rest) > 0 {
		id := rest[0]
		size, n := binary.Uvarint(rest[1:])
		if n <= 0 || uint64(len(rest)-1-n) < size {
			return nil, errors.New("a section runs past the end of the module")
		}
		body := rest[1+n : 1+n+int(size)]
		rest = rest[1+n+int(size):]
		if id == wasmExportSection {
			return readExportNames(body)
		}
	}
	return nil, errors.New("the module has no export section")
}

func readExportNames(body []byte) (map[string]bool, error) {
	count, n := binary.Uvarint(body)
	if n <= 0 {
		return nil, errors.New("the export section has no count")
	}
	body = body[n:]
	names := map[string]bool{}
	for range count {
		length, n := binary.Uvarint(body)
		if n <= 0 || uint64(len(body)-n) < length+1 {
			return nil, errors.New("an export name runs past the section")
		}
		names[string(body[n:n+int(length)])] = true
		body = body[n+int(length)+1:]
		if _, n = binary.Uvarint(body); n <= 0 {
			return nil, errors.New("an export has no index")
		}
		body = body[n:]
	}
	return names, nil
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

package providerkit

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

const FunctionImageRoot = "/ocel/app"

func FunctionImage(base v1.Image, runtime Runtime, dir string, overlay map[string][]byte) (v1.Image, error) {
	rels, err := artifactFiles(dir)
	if err != nil {
		return nil, err
	}
	packed, err := functionLayer(dir, rels, overlay)
	if err != nil {
		return nil, err
	}
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(packed)), nil
	})
	if err != nil {
		return nil, err
	}
	appended, err := mutate.Append(base, mutate.Addendum{Layer: layer})
	if err != nil {
		return nil, err
	}
	staged, err := functionStaging(dir)
	if err != nil {
		return nil, err
	}
	file, err := appended.ConfigFile()
	if err != nil {
		return nil, err
	}
	command, err := functionCommand(runtime, staged)
	if err != nil {
		return nil, err
	}
	config := file.Config
	config.Cmd = command
	config.WorkingDir = FunctionImageRoot
	config.Env = boundPort(config.Env)
	return mutate.Config(appended, config)
}

type functionStaged struct {
	Runtime Runtime  `json:"runtime"`
	Handler string   `json:"handler"`
	Command []string `json:"command,omitempty"`
	ID      string   `json:"id"`
}

func functionStaging(dir string) (functionStaged, error) {
	raw, err := os.ReadFile(filepath.Join(dir, functionConfigFile))
	if err != nil {
		return functionStaged{}, err
	}
	var staged functionStaged
	if err := json.Unmarshal(raw, &staged); err != nil {
		return functionStaged{}, err
	}
	return staged, nil
}

const functionConfigFile = "config.json"

// TODO(WP2): the host-neutral node membrane moves to frameworks/node and lands in the
// base image at this path; nothing writes it there yet.
const NodeMembranePath = "/ocel/membrane/entrypoint.mjs"

func boundPort(env []string) []string {
	kept := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if name, _, _ := strings.Cut(entry, "="); name == InjectedPortName {
			continue
		}
		kept = append(kept, entry)
	}
	return append(kept, InjectedPortName+"="+InjectedPortText)
}

func functionCommand(runtime Runtime, staged functionStaged) ([]string, error) {
	switch {
	case len(staged.Command) > 0:
		return staged.Command, nil
	case runtime.Name == RuntimeNode || runtime.Name == RuntimeNext:
		return []string{"node", NodeMembranePath}, nil
	default:
		return nil, Refuse(CodeInvalid,
			"the %s function staged at %s names no command to run, and only a node function boots through a membrane this image could run in its place",
			runtime.Name, staged.ID)
	}
}

func functionLayer(dir string, rels []string, overlay map[string][]byte) ([]byte, error) {
	var packed bytes.Buffer
	archive := tar.NewWriter(&packed)
	for _, rel := range rels {
		if err := tarFile(archive, filepath.Join(dir, filepath.FromSlash(rel)), rel); err != nil {
			return nil, err
		}
	}
	for _, rel := range overlayFiles(overlay) {
		if err := tarBody(archive, rel, overlay[rel], 0o644); err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return packed.Bytes(), nil
}

func tarFile(archive *tar.Writer, full, rel string) error {
	info, err := os.Lstat(full)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(full)
		if err != nil {
			return err
		}
		return archive.WriteHeader(&tar.Header{
			Typeflag: tar.TypeSymlink,
			Name:     imagePath(rel),
			Linkname: target,
		})
	}
	body, err := os.ReadFile(full)
	if err != nil {
		return err
	}
	mode := int64(0o644)
	if info.Mode()&0o100 != 0 {
		mode = 0o755
	}
	return tarBody(archive, rel, body, mode)
}

func tarBody(archive *tar.Writer, rel string, body []byte, mode int64) error {
	if err := archive.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     imagePath(rel),
		Mode:     mode,
		Size:     int64(len(body)),
	}); err != nil {
		return err
	}
	_, err := archive.Write(body)
	return err
}

func imagePath(rel string) string {
	return strings.TrimPrefix(path.Join(FunctionImageRoot, rel), "/")
}

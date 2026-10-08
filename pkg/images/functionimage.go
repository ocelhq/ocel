package images

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const FunctionImageRoot = "/ocel/app"

const StaticRoot = "/ocel/static"

type FunctionImageOptions struct {
	Overlay         map[string][]byte
	Runtime         map[string][]byte
	StaticSourceDir string
}

func FunctionImage(base v1.Image, framework buildoutput.Framework, dir string, opts FunctionImageOptions) (v1.Image, error) {
	rels, err := ArtifactFiles(dir)
	if err != nil {
		return nil, err
	}
	packed, err := functionLayer(dir, rels, opts)
	if err != nil {
		return nil, err
	}
	layer, err := newBytesLayer(packed)
	if err != nil {
		return nil, err
	}
	addenda := []mutate.Addendum{{Layer: layer}}
	if len(opts.Runtime) > 0 {
		runtimePacked, err := runtimeLayer(framework, opts.Runtime)
		if err != nil {
			return nil, err
		}
		runtime, err := newBytesLayer(runtimePacked)
		if err != nil {
			return nil, err
		}
		addenda = append([]mutate.Addendum{{Layer: runtime}}, addenda...)
	}
	appended, err := mutate.Append(base, addenda...)
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
	command, err := functionCommand(framework, staged)
	if err != nil {
		return nil, err
	}
	config := file.Config
	config.Cmd = command
	config.WorkingDir = FunctionImageRoot
	config.Env = boundPort(config.Env)
	if BootsThroughRuntime(framework) {
		config.Env = ensureNodeEnv(append(config.Env, servedHandler(staged)))
	}
	return mutate.Config(appended, config)
}

func functionStaging(dir string) (buildoutput.FunctionConfig, error) {
	raw, err := os.ReadFile(filepath.Join(dir, buildoutput.FunctionConfigFile))
	if err != nil {
		return buildoutput.FunctionConfig{}, err
	}
	var staged buildoutput.FunctionConfig
	if err := json.Unmarshal(raw, &staged); err != nil {
		return buildoutput.FunctionConfig{}, err
	}
	return staged, nil
}

const RuntimeEntrypointFile = "entrypoint.mjs"

const HandlerName = "OCEL_HANDLER"

const nodeEnvName = "NODE_ENV"

func BootsThroughRuntime(framework buildoutput.Framework) bool {
	return framework.Name == buildoutput.FrameworkNode || framework.Name == buildoutput.FrameworkNext
}

func servedHandler(staged buildoutput.FunctionConfig) string {
	return HandlerName + "=" + path.Join(FunctionImageRoot, staged.EntryFile)
}

func ensureNodeEnv(env []string) []string {
	for _, entry := range env {
		if name, _, _ := strings.Cut(entry, "="); name == nodeEnvName {
			return env
		}
	}
	return append(env, nodeEnvName+"=production")
}

func boundPort(env []string) []string {
	kept := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if name, _, _ := strings.Cut(entry, "="); name == containerimage.PortEnvVar {
			continue
		}
		kept = append(kept, entry)
	}
	return append(kept, containerimage.PortEnvVar+"="+containerimage.PortText)
}

func functionCommand(framework buildoutput.Framework, staged buildoutput.FunctionConfig) ([]string, error) {
	switch {
	case len(staged.Command) > 0:
		return staged.Command, nil
	case BootsThroughRuntime(framework):
		return []string{"node", path.Join(containerimage.FrameworkRuntimeDir(framework.Name), RuntimeEntrypointFile)}, nil
	default:
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"the %s function staged at %s names no command to run, and only a node or Next function boots through a runtime this image could run in its place",
			framework.Name, staged.ID)
	}
}

func runtimeLayer(framework buildoutput.Framework, runtime map[string][]byte) ([]byte, error) {
	dir := containerimage.FrameworkRuntimeDir(framework.Name)
	var packed bytes.Buffer
	archive := tar.NewWriter(&packed)
	for _, name := range sortedNames(runtime) {
		full := path.Join(dir, name)
		if path.IsAbs(name) || !strings.HasPrefix(full, dir+"/") {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"a function's image was handed the runtime file %s, and a runtime file is named by a path inside %s, which contains the runtime it boots through", name, dir)
		}
		if err := tarBody(archive, full, runtime[name], 0o644); err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return packed.Bytes(), nil
}

func functionLayer(dir string, rels []string, opts FunctionImageOptions) ([]byte, error) {
	overlay := opts.Overlay
	var packed bytes.Buffer
	archive := tar.NewWriter(&packed)
	for _, rel := range rels {
		if err := tarFile(archive, filepath.Join(dir, filepath.FromSlash(rel)), rel); err != nil {
			return nil, err
		}
	}
	for _, rel := range OverlayFiles(overlay) {
		if err := refuseStrayOverlay(rel); err != nil {
			return nil, err
		}
		if err := tarBody(archive, rel, overlay[rel], 0o644); err != nil {
			return nil, err
		}
	}
	if opts.StaticSourceDir != "" {
		if err := tarStatic(archive, opts.StaticSourceDir); err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return packed.Bytes(), nil
}

func tarStatic(archive *tar.Writer, staticSourceDir string) error {
	assets := filepath.Join(staticSourceDir, edge.StaticAssetDir)
	rels, err := ArtifactFiles(assets)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, rel := range rels {
		if err := tarFile(archive, filepath.Join(assets, filepath.FromSlash(rel)), path.Join(StaticRoot, naming.AssetsSegment, rel)); err != nil {
			return err
		}
	}
	err = tarFile(archive, filepath.Join(staticSourceDir, naming.ImageConfigFile), path.Join(StaticRoot, naming.ImageConfigFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
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

func refuseStrayOverlay(rel string) error {
	full := "/" + imagePath(rel)
	if strings.HasPrefix(full, FunctionImageRoot+"/") {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"a function's image was handed %s to include, and it lands at %s, outside %s, which contains the function's own tree: an image ocel builds writes nowhere else in the base it is built on",
		rel, full, FunctionImageRoot)
}

func imagePath(rel string) string {
	if path.IsAbs(rel) {
		return strings.TrimPrefix(path.Clean(rel), "/")
	}
	return strings.TrimPrefix(path.Join(FunctionImageRoot, rel), "/")
}

func FunctionRoute(app, function string) string {
	lead := naming.Join(naming.FieldSeparator, string(naming.KindFunction), app) + naming.FieldSeparator
	return naming.Sanitize(strings.TrimPrefix(function, lead))
}

package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	nameTaken  = "is already in use"
	readyTries = 120
	keptDir    = "kept"
)

func KeptPath(class providerkit.Class, name string) string {
	return StateDir(class) + "/" + keptDir + "/" + name
}

func keptCommand(path string) string {
	return "if [ -s " + quoted(path) + " ]; then cat " + quoted(path) + "; fi"
}

func keepCommand(path string) string {
	dir := path[:strings.LastIndex(path, "/")]
	return "set -e\n" +
		"umask 077\n" +
		"mkdir -p " + quoted(dir) + "\n" +
		"writing=$(mktemp " + quoted(dir+"/.writing.XXXXXX") + ")\n" +
		"cat >\"$writing\"\n" +
		"ln \"$writing\" " + quoted(path) + " 2>/dev/null || true\n" +
		"rm -f \"$writing\"\n" +
		"cat " + quoted(path)
}

func (h *Host) Kept(ctx context.Context, class providerkit.Class, name string) ([]byte, error) {
	said, err := h.ran(ctx, "read what is kept for "+name, keptCommand(KeptPath(class, name)), nil, "")
	if err != nil {
		return nil, err
	}
	return unkept(name, said)
}

func (h *Host) KeepOnce(ctx context.Context, class providerkit.Class, name string, candidate []byte) ([]byte, error) {
	fed := base64.StdEncoding.EncodeToString(candidate) + "\n"
	said, err := h.ran(ctx, "keep what "+name+" is held to", keepCommand(KeptPath(class, name)), strings.NewReader(fed), "")
	if err != nil {
		return nil, err
	}
	return unkept(name, said)
}

func unkept(name, said string) ([]byte, error) {
	kept, err := base64.StdEncoding.DecodeString(strings.TrimSpace(said))
	if err != nil {
		return nil, providerkit.Refuse(providerkit.CodeDenied,
			"what this box keeps for %s is %d bytes that nothing ocel wrote decodes to", name, len(said))
	}
	return kept, nil
}

type Volume struct {
	Path    string
	Driver  string
	Options map[string]string
}

type Credential struct {
	Env      string
	Reassert func(secret string) (argv []string, stdin string) `json:"-"`
}

type ResourceContainer struct {
	Name     string
	Project  string
	Resource string
	Class    providerkit.Class

	Image        string
	Args         []string
	Env          map[string]string
	Labels       map[string]string
	Capabilities []string
	Memory       string
	CPUs         string
	ShmSize      string

	Volume     Volume
	Credential Credential
	Ready      []string
}

func ResourceName(stack, resource, kind string) string {
	return naming.Sanitize(stack) + "-" + naming.Sanitize(resource) + "-" + kind
}

func (r ResourceContainer) digest() (string, error) {
	rendered, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(rendered)
	return hex.EncodeToString(sum[:])[:envDigestLen], nil
}

func (r ResourceContainer) labels() []string {
	argv := []string{
		"--label", LabelClass + "=" + string(r.Class),
		"--label", LabelProject + "=" + naming.Sanitize(r.Project),
		"--label", LabelResource + "=" + r.Resource,
	}
	for _, key := range slices.Sorted(maps.Keys(r.Labels)) {
		argv = append(argv, "--label", key+"="+r.Labels[key])
	}
	return argv
}

func volumeCreating(spec ResourceContainer) string {
	argv := append([]string{"docker", "volume", "create"}, spec.labels()...)
	if spec.Volume.Driver != "" {
		argv = append(argv, "--driver", spec.Volume.Driver)
	}
	for _, key := range slices.Sorted(maps.Keys(spec.Volume.Options)) {
		argv = append(argv, "--opt", key+"="+spec.Volume.Options[key])
	}
	return words(append(argv, spec.Name)) + " >/dev/null"
}

func resourceRun(spec ResourceContainer, digest, envFile string) []string {
	argv := []string{"docker", "run", "--detach",
		"--name", spec.Name,
		"--restart", appRestart,
		"--network", AppNetwork(spec.Class, spec.Project),
	}
	argv = append(argv, spec.labels()...)
	argv = append(argv, "--label", LabelRef+"="+spec.Image, "--label", LabelEnv+"="+digest)
	argv = append(argv, logging()...)
	argv = append(argv, confined(spec.Capabilities, false)...)
	for _, limit := range [][2]string{{"--memory", spec.Memory}, {"--cpus", spec.CPUs}, {"--shm-size", spec.ShmSize}} {
		if limit[1] != "" {
			argv = append(argv, limit[0], limit[1])
		}
	}
	argv = append(argv, "--env-file", envFile,
		"--mount", "type=volume,src="+spec.Name+",dst="+spec.Volume.Path, spec.Image)
	return append(argv, spec.Args...)
}

func readyCommand(spec ResourceContainer) string {
	probe := words(append([]string{"docker", "exec", spec.Name}, spec.Ready...))
	return "tries=0\n" +
		"until " + probe + " >/dev/null 2>&1; do\n" +
		"tries=$((tries+1))\n" +
		"if [ \"$tries\" -ge " + strconv.Itoa(readyTries) + " ]; then\n" +
		"printf '%s\\n' " + quoted(spec.Name+" never answered "+strings.Join(spec.Ready, " ")) + " >&2\n" +
		"docker logs --tail " + appLogTail + " " + quoted(spec.Name) + " >&2 || true\n" +
		"exit 1\n" +
		"fi\n" +
		"sleep 1\n" +
		"done"
}

func (h *Host) StandResource(ctx context.Context, spec ResourceContainer, secret string) (err error) {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	digest, err := spec.digest()
	if err != nil {
		return err
	}
	said := h.said(ctx, servingCommand(spec.Name), elevation)
	if stillServing(said, spec.Image, digest) {
		return nil
	}
	if said != "" {
		if _, err := h.ran(ctx, "clear the name "+spec.Name,
			"docker rm --force "+quoted(spec.Name)+" >/dev/null 2>&1 || true", nil, elevation); err != nil {
			return err
		}
	}
	if err := h.sweep(ctx, elevation); err != nil {
		return err
	}
	if err := h.joining(ctx, spec.Resource, spec.Class, spec.Project, networkCreating(spec.Class, spec.Project), elevation); err != nil {
		return err
	}
	if _, err := h.ran(ctx, "keep a volume for "+spec.Resource, volumeCreating(spec), nil, elevation); err != nil {
		return err
	}
	env := maps.Clone(spec.Env)
	if env == nil {
		env = map[string]string{}
	}
	env[spec.Credential.Env] = secret
	rendered, err := RenderEnvFile(env)
	if err != nil {
		return err
	}
	held := handoff{path: EnvFile(spec.Class, spec.Name)}
	defer func() { err = errors.Join(err, h.forget(ctx, held)) }()
	if _, err := h.ran(ctx, "write what "+spec.Resource+" is handed",
		"install -m 0600 /dev/stdin "+quoted(held.path), bytes.NewReader(rendered), ""); err != nil {
		return err
	}
	_, refused, stood := h.spoke(ctx, "stand "+spec.Resource+" up as "+spec.Name,
		words(resourceRun(spec, digest, held.path))+" >/dev/null", nil, elevation)
	if stood != nil && !strings.Contains(refused, nameTaken) {
		return stood
	}
	if _, err := h.ran(ctx, "wait for "+spec.Resource+" to answer", readyCommand(spec), nil, elevation); err != nil {
		return err
	}
	if spec.Credential.Reassert != nil {
		argv, stdin := spec.Credential.Reassert(secret)
		if _, err := h.ran(ctx, "hold "+spec.Resource+" to the credential it was handed",
			words(append([]string{"docker", "exec", "--interactive", spec.Name}, argv...))+" >/dev/null 2>&1",
			strings.NewReader(stdin), elevation); err != nil {
			return err
		}
	}
	return nil
}

func (h *Host) RemoveResource(ctx context.Context, class providerkit.Class, name string) error {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	volume := quoted(name)
	_, err = h.ran(ctx, "take "+name+" and its data down",
		"docker rm --force "+volume+" >/dev/null 2>&1 || true\n"+
			"docker volume rm "+volume+" >/dev/null 2>&1 || [ -z \"$(docker volume ls --quiet --filter "+quoted("name=^"+name+"$")+")\" ]", nil, elevation)
	if err != nil {
		return err
	}
	_, err = h.ran(ctx, "forget what "+name+" was held to", "rm -f "+quoted(KeptPath(class, name)), nil, "")
	return err
}

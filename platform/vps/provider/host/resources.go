package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
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
	return "if [ -e " + quoted(stateRoot) + " ] && [ ! -x " + quoted(stateRoot) + " ]; then echo " + quoted(stateRoot+": Permission denied") + " >&2; exit 1; fi\n" +
		"if [ -s " + quoted(path) + " ]; then cat " + quoted(path) + "; fi"
}

func (h *Host) unhand(ctx context.Context, held handoff) error {
	taking, stop := context.WithTimeout(context.WithoutCancel(ctx), forgetWindow)
	defer stop()
	if _, err := h.owning(taking, "take back "+held.path, "rm -f "+quoted(held.path), nil); err != nil {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"could not remove %s on %s, which holds a plaintext credential: %v",
			held.path, h.named(), err)
	}
	return nil
}

func handedBack(class providerkit.Class, path string) string {
	return "if [ \"$(id -u)\" -eq 0 ]; then chown -R --reference=" + quoted(StateDir(class)) + " " + quoted(path) + "; fi"
}

func (h *Host) owning(ctx context.Context, what, command string, stdin []byte) (string, error) {
	fed := func() io.Reader {
		if stdin == nil {
			return nil
		}
		return bytes.NewReader(stdin)
	}
	said, refused, err := h.spoke(ctx, what, command, fed(), "")
	if err == nil || !strings.Contains(strings.ToLower(refused), "permission denied") {
		return said, err
	}
	elevation, unelevated := h.elevate(ctx)
	if unelevated != nil || elevation == "" {
		return said, err
	}
	return h.ran(ctx, what, command, fed(), elevation)
}

func keepCommand(class providerkit.Class, path string) string {
	dir := path[:strings.LastIndex(path, "/")]
	return "set -e\n" +
		"umask 077\n" +
		"mkdir -p " + quoted(dir) + "\n" +
		"writing=$(mktemp " + quoted(dir+"/.writing.XXXXXX") + ")\n" +
		"cat >\"$writing\"\n" +
		"ln \"$writing\" " + quoted(path) + " 2>/dev/null || true\n" +
		"rm -f \"$writing\"\n" +
		handedBack(class, dir) + "\n" +
		"cat " + quoted(path)
}

func (h *Host) ForgetKept(ctx context.Context, class providerkit.Class, names []string) error {
	if len(names) == 0 {
		return nil
	}
	paths := make([]string, 0, len(names))
	for _, name := range names {
		paths = append(paths, quoted(KeptPath(class, name)))
	}
	_, err := h.owning(ctx, "forget what "+strings.Join(names, ", ")+" was held to",
		"rm -f "+strings.Join(paths, " "), nil)
	return err
}

func (h *Host) Kept(ctx context.Context, class providerkit.Class, name string) ([]byte, error) {
	said, err := h.owning(ctx, "read what is kept for "+name, keptCommand(KeptPath(class, name)), nil)
	if err != nil {
		return nil, err
	}
	return unkept(name, said)
}

func (h *Host) KeepOnce(ctx context.Context, class providerkit.Class, name string, candidate []byte) ([]byte, error) {
	fed := base64.StdEncoding.EncodeToString(candidate) + "\n"
	said, err := h.owning(ctx, "keep what "+name+" is held to", keepCommand(class, KeptPath(class, name)), []byte(fed))
	if err != nil {
		return nil, err
	}
	return unkept(name, said)
}

func unkept(name, said string) ([]byte, error) {
	kept, err := base64.StdEncoding.DecodeString(strings.TrimSpace(said))
	if err != nil {
		return nil, providerkit.Refuse(providerkit.CodeDenied,
			"the value kept for %s is %d undecodable bytes", name, len(said))
	}
	return kept, nil
}

type Volume struct {
	Path       string
	Driver     string
	Options    map[string]string
	Generation string
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
	Backup     string
	Database   string
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

const retiredSuffix = "-retired"

func volumeName(name, generation string) string {
	if generation == "" {
		return name
	}
	return name + "-g" + naming.Sanitize(generation)
}

func (r ResourceContainer) volume() string { return volumeName(r.Name, r.Volume.Generation) }

func (r ResourceContainer) VolumeName() string { return r.volume() }

func volumesOf(class providerkit.Class, project, resource, name string) string {
	return "docker volume ls --filter " + quoted("name=^"+name) +
		" --filter " + quoted("label="+LabelClass+"="+string(class)) +
		" --filter " + quoted("label="+LabelProject+"="+naming.Sanitize(project)) +
		" --filter " + quoted("label="+LabelResource+"="+resource)
}

func volumeCreating(spec ResourceContainer) string {
	argv := append([]string{"docker", "volume", "create"}, spec.labels()...)
	if spec.Volume.Generation != "" {
		argv = append(argv, "--label", LabelGeneration+"="+spec.Volume.Generation)
	}
	if spec.Volume.Driver != "" {
		argv = append(argv, "--driver", spec.Volume.Driver)
	}
	for _, key := range slices.Sorted(maps.Keys(spec.Volume.Options)) {
		argv = append(argv, "--opt", key+"="+spec.Volume.Options[key])
	}
	return words(append(argv, spec.volume())) + " >/dev/null"
}

func resourceStanding(spec ResourceContainer, digest, envFile string) string {
	return imageHeld(spec.Image, appPulls) + words(resourceRun(spec, digest, envFile)) + " >/dev/null"
}

func resourceRun(spec ResourceContainer, digest, envFile string) []string {
	argv := []string{"docker", "run", "--detach",
		"--name", spec.Name,
		"--restart", appRestart,
		"--network", AppNetwork(spec.Class, spec.Project),
	}
	argv = append(argv, spec.labels()...)
	argv = append(argv, "--label", LabelRef+"="+spec.Image, "--label", LabelEnv+"="+digest)
	if spec.Backup != "" {
		argv = append(argv, "--label", LabelBackup+"="+spec.Backup)
	}
	argv = append(argv, logging()...)
	argv = append(argv, confined(spec.Capabilities, false)...)
	for _, limit := range [][2]string{{"--memory", spec.Memory}, {"--cpus", spec.CPUs}, {"--shm-size", spec.ShmSize}} {
		if limit[1] != "" {
			argv = append(argv, limit[0], limit[1])
		}
	}
	argv = append(argv, "--env-file", envFile,
		"--mount", "type=volume,src="+spec.volume()+",dst="+spec.Volume.Path, spec.Image)
	return append(argv, spec.Args...)
}

func generationCommand(spec ResourceContainer) string {
	return volumesOf(spec.Class, spec.Project, spec.Resource, spec.Name) +
		" --format " + quoted(`{{.Label "`+LabelGeneration+`"}}`)
}

func (h *Host) initialisedUnder(ctx context.Context, spec ResourceContainer, elevation string) (string, error) {
	if spec.Volume.Generation == "" {
		return "", nil
	}
	said, err := h.ran(ctx, "read what initialised "+spec.Resource+"'s data", generationCommand(spec), nil, elevation)
	if err != nil {
		return "", err
	}
	held := strings.Fields(said)
	if len(held) == 0 || slices.Contains(held, spec.Volume.Generation) {
		return "", nil
	}
	return held[0], nil
}

func swapCommand(spec ResourceContainer, from, digest, envFile, dump string) string {
	name, retired := quoted(spec.Name), quoted(spec.Name+retiredSuffix)
	fresh, stale := quoted(spec.volume()), quoted(volumeName(spec.Name, from))
	return "set -eu\n" +
		"swapped=0\n" +
		"back() {\n" +
		"if [ \"$swapped\" -eq 0 ]; then\n" +
		"docker rm --force " + name + " >/dev/null 2>&1 || true\n" +
		"docker volume rm " + fresh + " >/dev/null 2>&1 || true\n" +
		"docker rename " + retired + " " + name + " >/dev/null 2>&1 || true\n" +
		"docker start " + name + " >/dev/null 2>&1 || true\n" +
		"fi\n" +
		"}\n" +
		"trap back EXIT\n" +
		"trap 'exit 1' HUP INT TERM\n" +
		"docker stop " + name + " >/dev/null\n" +
		"docker rename " + name + " " + retired + "\n" +
		volumeCreating(spec) + "\n" +
		words(resourceRun(spec, digest, envFile)) + " >/dev/null\n" +
		readyCommand(spec) + "\n" +
		restoreCommand(spec.Class, spec.Name, spec.Database, dump) + "\n" +
		"swapped=1\n" +
		"docker rm --force " + retired + " >/dev/null\n" +
		"docker volume rm " + stale + " >/dev/null"
}

func (h *Host) upgrade(ctx context.Context, spec ResourceContainer, from, digest, secret, elevation string) (err error) {
	dumped, err := h.ran(ctx, "dump "+spec.Resource+" while version "+from+" still serves it",
		dumpCommand(spec.Class, spec.Name, spec.Database), nil, elevation)
	if err != nil {
		return err
	}
	held, err := h.handResource(ctx, spec, secret)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, h.unhand(ctx, held)) }()
	if _, err := h.ran(ctx, "move "+spec.Resource+" from version "+from+" to "+spec.Volume.Generation,
		swapCommand(spec, from, digest, held.path, strings.TrimSpace(dumped)), nil, elevation); err != nil {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"%s could not move from version %s to %s and is back on %s, data intact: %v",
			spec.Resource, from, spec.Volume.Generation, from, err)
	}
	return nil
}

func (h *Host) handResource(ctx context.Context, spec ResourceContainer, secret string) (handoff, error) {
	env := maps.Clone(spec.Env)
	if env == nil {
		env = map[string]string{}
	}
	env[spec.Credential.Env] = secret
	rendered, err := RenderEnvFile(env)
	if err != nil {
		return handoff{}, err
	}
	held := handoff{path: EnvFile(spec.Class, spec.Name)}
	_, err = h.owning(ctx, "write what "+spec.Resource+" is handed",
		"install -m 0600 /dev/stdin "+quoted(held.path), rendered)
	return held, err
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
	if err := h.joining(ctx, spec.Resource, spec.Class, spec.Project, networkStanding(spec.Class, spec.Project), elevation); err != nil {
		return err
	}
	said := h.said(ctx, servingCommand(spec.Name), elevation)
	if stillServing(said, spec.Image, digest) {
		return nil
	}
	from, err := h.initialisedUnder(ctx, spec, elevation)
	if err != nil {
		return err
	}
	if from != "" {
		if !strings.HasPrefix(said, "running ") {
			return providerkit.Refuse(providerkit.CodeNotReady,
				"%s holds version %s data, this deploy declares %s, and %s is not running to dump it\n"+
					"Run `docker start %s` or set the version back to %s",
				spec.Resource, from, spec.Volume.Generation, spec.Name, spec.Name, from)
		}
		return h.upgrade(ctx, spec, from, digest, secret, elevation)
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
	if _, err := h.ran(ctx, "keep a volume for "+spec.Resource, volumeCreating(spec), nil, elevation); err != nil {
		return err
	}
	held, err := h.handResource(ctx, spec, secret)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, h.unhand(ctx, held)) }()
	_, refused, stood := h.spoke(ctx, "stand "+spec.Resource+" up as "+spec.Name,
		resourceStanding(spec, digest, held.path), nil, elevation)
	if stood != nil && !strings.Contains(refused, nameTaken) {
		return stood
	}
	if _, err := h.ran(ctx, "wait for "+spec.Resource+" to answer", readyCommand(spec), nil, elevation); err != nil {
		return err
	}
	if spec.Credential.Reassert != nil {
		argv, stdin := spec.Credential.Reassert(secret)
		if _, err := h.ran(ctx, "reassert "+spec.Resource+"'s credential",
			words(append([]string{"docker", "exec", "--interactive", spec.Name}, argv...))+" >/dev/null 2>&1",
			strings.NewReader(stdin), elevation); err != nil {
			return err
		}
	}
	return nil
}

type ResourceRef struct {
	Class    providerkit.Class
	Project  string
	Resource string
	Name     string
}

func (h *Host) RemoveResource(ctx context.Context, ref ResourceRef) error {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	volumes := volumesOf(ref.Class, ref.Project, ref.Resource, ref.Name) + " --quiet"
	_, err = h.ran(ctx, "take "+ref.Name+" and its data down",
		"docker rm --force "+quoted(ref.Name)+" "+quoted(ref.Name+retiredSuffix)+" >/dev/null 2>&1 || true\n"+
			volumes+" | xargs -r docker volume rm >/dev/null\n"+
			"[ -z \"$("+volumes+")\" ]", nil, elevation)
	if err != nil {
		return err
	}
	_, err = h.ran(ctx, "remove "+ref.Name+"'s kept credential and backups",
		"rm -f "+quoted(KeptPath(ref.Class, ref.Name))+"\nrm -rf "+quoted(BackupsDir(ref.Class, ref.Name)), nil, elevation)
	return err
}

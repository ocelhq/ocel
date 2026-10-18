package container

import (
	"bytes"
	"cmp"
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

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/envfile"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const (
	nameTaken = "is already in use"

	ResourceStopSeconds = 60
	keptDir             = "kept"

	resourceNameMax = 63
)

type Containers struct {
	box    *ssh.Box
	engine *docker.Engine
}

func New(box *ssh.Box, engine *docker.Engine) *Containers {
	return &Containers{box: box, engine: engine}
}

func KeptPath(tier environment.Tier, name string) string {
	return boxapi.StateDir(tier) + "/" + keptDir + "/" + name
}

func keptCommand(path string) string {
	return "if [ -e " + shell.Quote(boxapi.StateRoot) + " ] && [ ! -x " + shell.Quote(boxapi.StateRoot) + " ]; then echo " + shell.Quote(boxapi.StateRoot+": Permission denied") + " >&2; exit 1; fi\n" +
		"if [ -s " + shell.Quote(path) + " ]; then cat " + shell.Quote(path) + "; fi"
}

func (c *Containers) takeBack(ctx context.Context, delivery envfile.Delivery) error {
	taking, stop := context.WithTimeout(context.WithoutCancel(ctx), envfile.ForgetWindow)
	defer stop()
	if _, err := c.runAsDeploy(taking, "take back "+delivery.Path, "rm -f "+shell.Quote(delivery.Path), nil); err != nil {
		return refusal.Refuse(refusal.CodeNotReady,
			"could not remove %s on %s, which contains a plaintext credential: %v",
			delivery.Path, c.box.Named(), err)
	}
	return nil
}

func handedBack(tier environment.Tier, path string) string {
	return "if [ \"$(id -u)\" -eq 0 ]; then chown -R --reference=" + shell.Quote(boxapi.StateDir(tier)) + " " + shell.Quote(path) + "; fi"
}

func (c *Containers) runAsDeploy(ctx context.Context, what, command string, stdin []byte) (string, error) {
	var fed io.Reader
	if stdin != nil {
		fed = bytes.NewReader(stdin)
	}
	return c.box.Run(ctx, ssh.Deploy, what, command, fed)
}

func keepCommand(tier environment.Tier, path string) string {
	dir := path[:strings.LastIndex(path, "/")]
	return "set -e\n" +
		"umask 077\n" +
		"mkdir -p " + shell.Quote(dir) + "\n" +
		"writing=$(mktemp " + shell.Quote(dir+"/.writing.XXXXXX") + ")\n" +
		"cat >\"$writing\"\n" +
		"ln \"$writing\" " + shell.Quote(path) + " 2>/dev/null || true\n" +
		"rm -f \"$writing\"\n" +
		handedBack(tier, dir) + "\n" +
		"cat " + shell.Quote(path)
}

func (c *Containers) ForgetKept(ctx context.Context, tier environment.Tier, names []string) error {
	if len(names) == 0 {
		return nil
	}
	paths := make([]string, 0, len(names))
	for _, name := range names {
		paths = append(paths, shell.Quote(KeptPath(tier, name)))
	}
	_, err := c.runAsDeploy(ctx, "forget what "+strings.Join(names, ", ")+" was bound to",
		"rm -f "+strings.Join(paths, " "), nil)
	return err
}

func (c *Containers) Kept(ctx context.Context, tier environment.Tier, name string) ([]byte, error) {
	said, err := c.runAsDeploy(ctx, "read what is kept for "+name, keptCommand(KeptPath(tier, name)), nil)
	if err != nil {
		return nil, err
	}
	return unkept(name, said)
}

func (c *Containers) KeepOnce(ctx context.Context, tier environment.Tier, name string, candidate []byte) ([]byte, error) {
	fed := base64.StdEncoding.EncodeToString(candidate) + "\n"
	said, err := c.runAsDeploy(ctx, "keep what "+name+" is bound to", keepCommand(tier, KeptPath(tier, name)), []byte(fed))
	if err != nil {
		return nil, err
	}
	return unkept(name, said)
}

func unkept(name, said string) ([]byte, error) {
	kept, err := base64.StdEncoding.DecodeString(strings.TrimSpace(said))
	if err != nil {
		return nil, refusal.Refuse(refusal.CodeDenied,
			"the value kept for %s is %d undecodable bytes", name, len(said))
	}
	return kept, nil
}

type Volume struct {
	Path       string
	Driver     string
	Options    map[string]string
	Generation string

	CopyOnUpgrade bool
}

type Credential struct {
	Env      string
	Reassert func(secret string) (argv []string, stdin string) `json:"-"`
}

type Mount struct {
	Source string
	Target string
}

type Spec struct {
	Name     string
	Project  string
	Resource string
	Tier     environment.Tier

	Image        string
	Args         []string
	User         string
	Env          map[string]string
	Labels       map[string]string
	Capabilities []string
	Memory       string
	CPUs         string
	ShmSize      string
	ReadOnly     bool
	Mounts       []Mount

	Volume     Volume
	Sockets    string
	Credential Credential
	Ready      []string
	Backup     string
}

const socketsSuffix = "-sockets"

func (r Spec) socketsVolume() string { return r.Name + socketsSuffix }

func socketsCreating(spec Spec) string {
	if spec.Sockets == "" {
		return ""
	}
	return shell.Words(append(append([]string{"docker", "volume", "create"}, spec.labels()...), spec.socketsVolume())) + " >/dev/null\n"
}

func Name(project, stack, resource, kind string) string {
	return naming.Fit(resourceNameMax, naming.WordSeparator,
		naming.Compressible(project), naming.Fixed(stack), naming.Fixed(resource), naming.Fixed(kind))
}

func (r Spec) Digest() (string, error) {
	rendered, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(rendered)
	return hex.EncodeToString(sum[:])[:envfile.DigestLen], nil
}

func (r Spec) labels() []string {
	argv := []string{
		"--label", docker.LabelTier + "=" + string(r.Tier),
		"--label", docker.LabelProject + "=" + naming.Sanitize(r.Project),
		"--label", docker.LabelResource + "=" + r.Resource,
	}
	for _, key := range slices.Sorted(maps.Keys(r.Labels)) {
		argv = append(argv, "--label", key+"="+r.Labels[key])
	}
	return argv
}

const (
	RetiredSuffix = "-retired"
	SwappedMark   = "swapped"
)

func volumeName(name, generation string) string {
	if generation == "" {
		return name
	}
	return name + "-g" + naming.Sanitize(generation)
}

func (r Spec) volume() string { return volumeName(r.Name, r.Volume.Generation) }

func (r Spec) VolumeName() string { return r.volume() }

func volumesOf(tier environment.Tier, project, resource, name string) string {
	return "docker volume ls --filter " + shell.Quote("name=^"+name) +
		" --filter " + shell.Quote("label="+docker.LabelTier+"="+string(tier)) +
		" --filter " + shell.Quote("label="+docker.LabelProject+"="+naming.Sanitize(project)) +
		" --filter " + shell.Quote("label="+docker.LabelResource+"="+resource)
}

func volumeCreating(spec Spec) string {
	argv := append([]string{"docker", "volume", "create"}, spec.labels()...)
	if spec.Volume.Generation != "" {
		argv = append(argv, "--label", docker.LabelGeneration+"="+spec.Volume.Generation)
	}
	if spec.Volume.Driver != "" {
		argv = append(argv, "--driver", spec.Volume.Driver)
	}
	for _, key := range slices.Sorted(maps.Keys(spec.Volume.Options)) {
		argv = append(argv, "--opt", key+"="+spec.Volume.Options[key])
	}
	return shell.Words(append(argv, spec.volume())) + " >/dev/null"
}

func RunScript(spec Spec, digest, envFile string) string {
	return docker.Pulled(spec.Image, docker.PullAttempts, shell.PullAttemptSeconds, shell.PullBudgetSeconds) +
		socketsCreating(spec) +
		shell.Bounded(shell.DockerStepSeconds, "docker run of "+spec.Name, shell.Words(Run(spec, digest, envFile))+" >/dev/null")
}

func Run(spec Spec, digest, envFile string) []string {
	argv := []string{"docker", "run", "--detach",
		"--name", spec.Name,
		"--restart", docker.RestartPolicy,
		"--network", boxapi.AppNetwork(spec.Tier, spec.Project),
	}
	argv = append(argv, spec.labels()...)
	argv = append(argv, "--label", docker.LabelRef+"="+spec.Image, "--label", docker.LabelEnv+"="+digest)
	if spec.Backup != "" {
		argv = append(argv, "--label", boxapi.LabelBackup+"="+spec.Backup)
	}
	argv = append(argv, docker.Logging()...)
	argv = append(argv, docker.Confined(spec.Capabilities, false)...)
	if spec.User != "" {
		argv = append(argv, "--user", spec.User)
	}
	for _, limit := range [][2]string{{"--memory", spec.Memory}, {"--cpus", spec.CPUs}, {"--shm-size", spec.ShmSize}} {
		if limit[1] != "" {
			argv = append(argv, limit[0], limit[1])
		}
	}
	if spec.ReadOnly {
		argv = append(argv, "--read-only")
	}
	argv = append(argv, "--env-file", envFile)
	for _, mount := range spec.Mounts {
		argv = append(argv, "--mount", docker.BoundMount(mount.Source, mount.Target, "readonly"))
	}
	if spec.Volume.Path != "" {
		argv = append(argv, "--mount", "type=volume,src="+spec.volume()+",dst="+spec.Volume.Path)
	}
	if spec.Sockets != "" {
		argv = append(argv, "--mount", "type=volume,src="+spec.socketsVolume()+",dst="+spec.Sockets)
	}
	argv = append(argv, spec.Image)
	return append(argv, spec.Args...)
}

func generationCommand(spec Spec) string {
	return volumesOf(spec.Tier, spec.Project, spec.Resource, spec.Name) +
		" --format " + shell.Quote(`{{.Label "`+docker.LabelGeneration+`"}}`)
}

func (c *Containers) initialisedUnder(ctx context.Context, spec Spec, elevation ssh.Acting) (string, error) {
	if spec.Volume.Generation == "" {
		return "", nil
	}
	said, err := c.box.Run(ctx, elevation, "read what initialised "+spec.Resource+"'s data", generationCommand(spec), nil)
	if err != nil {
		return "", err
	}
	generations := strings.Fields(said)
	if len(generations) == 0 || slices.Contains(generations, spec.Volume.Generation) {
		return "", nil
	}
	return generations[0], nil
}

func SwapCommand(spec Spec, from, digest, envFile string, steps UpgradeSteps, secret string) (command, stdin string) {
	name, retired := shell.Quote(spec.Name), shell.Quote(spec.Name+RetiredSuffix)
	fresh, stale := shell.Quote(spec.volume()), shell.Quote(volumeName(spec.Name, from))
	moving := fresh != stale
	authenticated := ""
	if spec.Credential.Reassert != nil {
		var argv []string
		argv, stdin = spec.Credential.Reassert(secret)
		authenticated = reassertCommand(spec.Name, argv) + "\n"
	}
	dropFresh, createFresh, dropStale := "", "", ""
	if moving {
		dropFresh = "docker volume rm " + fresh + " >/dev/null 2>&1 || true\n"
		createFresh = volumeCreating(spec) + "\n"
		dropStale = "\ndocker volume rm " + stale + " >/dev/null"
	}
	return "set -eu\n" +
		"swapped=0\n" +
		"renamed=0\n" +
		"back() {\n" +
		"if [ \"$swapped\" -eq 0 ]; then\n" +
		"if [ \"$renamed\" -eq 1 ]; then\n" +
		"docker rm --force " + name + " >/dev/null 2>&1 || true\n" +
		dropFresh +
		"docker rename " + retired + " " + name + " >/dev/null 2>&1 || true\n" +
		"fi\n" +
		"docker start " + name + " >/dev/null 2>&1 || true\n" +
		"fi\n" +
		"}\n" +
		"docker rm --force " + retired + " >/dev/null 2>&1 || true\n" +
		"trap back EXIT\n" +
		"trap 'exit 1' HUP INT TERM\n" +
		shell.Bounded(shell.DockerStepSeconds, "docker stop of "+spec.Name, "docker stop --time "+strconv.Itoa(ResourceStopSeconds)+" "+name+" >/dev/null") + "\n" +
		"docker rename " + name + " " + retired + "\n" +
		"renamed=1\n" +
		createFresh +
		socketsCreating(spec) +
		steps.Before +
		shell.Bounded(shell.DockerStepSeconds, "docker run of "+spec.Name, shell.Words(Run(spec, digest, envFile))+" >/dev/null") + "\n" +
		ReadyCommand(spec, shell.ReadySeconds) + "\n" +
		authenticated +
		steps.After +
		"swapped=1\n" +
		"printf '%s\\n' " + SwappedMark + "\n" +
		"docker rm --force " + retired + " >/dev/null" +
		dropStale, stdin
}

func reassertCommand(name string, argv []string) string {
	return shell.Words(append([]string{"docker", "exec", "--interactive", name}, argv...)) + " >/dev/null 2>&1"
}

type UpgradeSteps struct {
	Before string
	After  string
}

func VolumeCopy(spec Spec, from string) UpgradeSteps {
	argv := []string{"docker", "run", "--rm", "--network", "none", "--cap-drop", "ALL"}
	if spec.User != "" {
		argv = append(argv, "--user", spec.User)
	}
	argv = append(argv,
		"--mount", "type=volume,src="+volumeName(spec.Name, from)+",dst=/from,readonly",
		"--mount", "type=volume,src="+spec.volume()+",dst="+spec.Volume.Path,
		"--entrypoint", "cp", spec.Image, "-a", "/from/.", spec.Volume.Path+"/")
	return UpgradeSteps{Before: shell.Words(argv) + "\n"}
}

func (c *Containers) prepareUpgrade(ctx context.Context, spec Spec, from string, elevation ssh.Acting) (UpgradeSteps, error) {
	if spec.Volume.CopyOnUpgrade {
		return VolumeCopy(spec, from), nil
	}
	dumped, err := c.box.Run(ctx, elevation, "dump "+spec.Resource+" while version "+from+" still serves it", boxapi.DumpCommand(spec.Tier, spec.Name), nil)
	if err != nil {
		return UpgradeSteps{}, err
	}
	return UpgradeSteps{After: boxapi.RestoreCommand(spec.Tier, spec.Name, strings.TrimSpace(dumped)) + "\n"}, nil
}

func (c *Containers) upgrade(ctx context.Context, spec Spec, from, digest, secret string, elevation ssh.Acting) (err error) {
	steps, err := c.prepareUpgrade(ctx, spec, from, elevation)
	if err != nil {
		return err
	}
	delivery, err := c.hand(ctx, spec, secret)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, c.takeBack(ctx, delivery)) }()
	swap, stdin := SwapCommand(spec, from, digest, delivery.Path, steps, secret)
	switch swapped, err := c.swap(ctx, elevation, "move "+spec.Resource+" from version "+from+" to "+spec.Volume.Generation, swap, stdin); {
	case swapped && err != nil:
		return refusal.Refuse(refusal.CodeNotReady,
			"%s moved from version %s to %s, but removing %s and the volume %s afterwards failed: %v",
			spec.Resource, from, spec.Volume.Generation, spec.Name+RetiredSuffix, volumeName(spec.Name, from), err)
	case err != nil:
		return refusal.Refuse(refusal.CodeNotReady,
			"%s could not move from version %s to %s and is back on %s, data intact: %v",
			spec.Resource, from, spec.Volume.Generation, from, err)
	}
	return nil
}

func (c *Containers) swap(ctx context.Context, elevation ssh.Acting, what, command, stdin string) (swapped bool, err error) {
	result, err := c.box.Stream(ctx, elevation, command, strings.NewReader(stdin))
	if err != nil {
		return false, err
	}
	if result.Code == 0 {
		return true, nil
	}
	said := strings.Fields(result.Stdout)
	return len(said) > 0 && said[len(said)-1] == SwappedMark, c.box.Refuse(what, result)
}

func (c *Containers) Limited(ctx context.Context, spec Spec) (Spec, error) {
	limits, err := c.engine.Limits(ctx, spec.Tier)
	if err != nil {
		return Spec{}, err
	}
	if spec.Memory == "" {
		spec.Memory = strconv.FormatInt(limits.MemoryBytes, 10)
	}
	if spec.CPUs == "" {
		spec.CPUs = limits.CPUs
	}
	return spec, nil
}

func (c *Containers) replace(ctx context.Context, spec Spec, digest, secret string, elevation ssh.Acting) (err error) {
	if err := envfile.Sweep(ctx, c.box, elevation); err != nil {
		return err
	}
	delivery, err := c.hand(ctx, spec, secret)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, c.takeBack(ctx, delivery)) }()
	swap, stdin := SwapCommand(spec, spec.Volume.Generation, digest, delivery.Path, UpgradeSteps{}, secret)
	switch swapped, err := c.swap(ctx, elevation, "replace "+spec.Resource+"'s container "+spec.Name, swap, stdin); {
	case swapped && err != nil:
		return refusal.Refuse(refusal.CodeNotReady,
			"%s's new container took over, but removing its previous container %s afterwards failed: %v",
			spec.Resource, spec.Name+RetiredSuffix, err)
	case err != nil:
		return refusal.Refuse(refusal.CodeNotReady,
			"%s's new container did not take over, so its previous container is back, data intact: %v", spec.Resource, err)
	}
	return nil
}

func (c *Containers) hand(ctx context.Context, spec Spec, secret string) (envfile.Delivery, error) {
	env := maps.Clone(spec.Env)
	if env == nil {
		env = map[string]string{}
	}
	if spec.Credential.Env != "" {
		env[spec.Credential.Env] = secret
	}
	rendered, err := envfile.Render(env)
	if err != nil {
		return envfile.Delivery{}, err
	}
	delivery := envfile.Delivery{Path: envfile.Path(spec.Tier, spec.Name)}
	_, err = c.runAsDeploy(ctx, "write what "+spec.Resource+" is handed",
		envfile.PipedInstall("-m 0600", delivery.Path), rendered)
	return delivery, err
}

func ReadyCommand(spec Spec, seconds int) string {
	probe := shell.Within(min(shell.ProbeSeconds, seconds), shell.Words(append([]string{"docker", "exec", spec.Name}, spec.Ready...)))
	return "deadline=$(($(date +%s) + " + strconv.Itoa(seconds) + "))\n" +
		"until " + probe + " >/dev/null 2>&1; do\n" +
		"if [ \"$(date +%s)\" -ge \"$deadline\" ]; then\n" +
		"printf '%s\\n' " + shell.Quote(spec.Name+" never answered "+strings.Join(spec.Ready, " ")+" within "+shell.Seconds(seconds)) + " >&2\n" +
		shell.Within(shell.ProbeSeconds, "docker logs --tail "+docker.LogTail+" "+shell.Quote(spec.Name)) + " >&2 || true\n" +
		"exit 1\n" +
		"fi\n" +
		"sleep 1\n" +
		"done"
}

type reading struct {
	spec    Spec
	digest  string
	serving string
	from    string
}

func (c *Containers) read(ctx context.Context, spec Spec, elevation ssh.Acting) (reading, error) {
	spec, err := c.Limited(ctx, spec)
	if err != nil {
		return reading{}, err
	}
	digest, err := spec.Digest()
	if err != nil {
		return reading{}, err
	}
	said, err := c.engine.ReadServing(ctx, spec.Name)
	if err != nil {
		return reading{}, err
	}
	read := reading{spec: spec, digest: digest, serving: said}
	if read.isCurrent() {
		return read, nil
	}
	if read.from, err = c.initialisedUnder(ctx, spec, elevation); err != nil {
		return reading{}, err
	}
	return read, read.refuseUpgradeOverStoppedServer()
}

func (r reading) isCurrent() bool { return docker.IsServing(r.serving, r.spec.Image, r.digest) }

func (r reading) refuseUpgradeOverStoppedServer() error {
	if r.from == "" || r.spec.Volume.CopyOnUpgrade || strings.HasPrefix(r.serving, "running ") {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"%s has version %s data, this deploy declares %s, and %s is not running to dump it\n"+
			"Run `docker start %s` or set the version back to %s",
		r.spec.Resource, r.from, r.spec.Volume.Generation, r.spec.Name, r.spec.Name, r.from)
}

func (r reading) planChange() (provider.ChangeAction, string) {
	fields := append(strings.Fields(r.serving), "", "")
	switch status, image := fields[0], fields[1]; {
	case r.isCurrent():
		return provider.ActionKeep, provider.ReasonCurrent
	case r.from != "":
		return provider.ActionReplace, "its data moves from version " + r.from + " to " + r.spec.Volume.Generation
	case status == "":
		return provider.ActionCreate, ""
	case image != r.spec.Image:
		return provider.ActionReplace, "its image changes from " + cmp.Or(image, "an unlabelled one") + " to " + r.spec.Image
	case status != "running":
		return provider.ActionReplace, "its container " + r.spec.Name + " is " + status + ", so a new one is started"
	default:
		return provider.ActionReplace, "the settings of its container " + r.spec.Name + " change"
	}
}

func (c *Containers) Plan(ctx context.Context, spec Spec) (provider.ChangeAction, string, error) {
	elevation, err := ssh.Docker, c.box.Assume(ctx, ssh.Docker)
	if err != nil {
		return "", "", err
	}
	read, err := c.read(ctx, spec, elevation)
	if err != nil {
		return "", "", err
	}
	action, reason := read.planChange()
	return action, reason, nil
}

func (c *Containers) Run(ctx context.Context, spec Spec, secret string) (err error) {
	elevation, err := ssh.Docker, c.box.Assume(ctx, ssh.Docker)
	if err != nil {
		return err
	}
	if err := c.engine.Join(ctx, spec.Resource, spec.Tier, spec.Project); err != nil {
		return err
	}
	read, err := c.read(ctx, spec, elevation)
	if err != nil {
		return err
	}
	spec, digest := read.spec, read.digest
	switch {
	case read.isCurrent():
		return nil
	case read.from != "":
		return c.upgrade(ctx, spec, read.from, digest, secret, elevation)
	case read.serving != "":
		return c.replace(ctx, spec, digest, secret, elevation)
	}
	if err := envfile.Sweep(ctx, c.box, elevation); err != nil {
		return err
	}
	if spec.Volume.Path != "" {
		if _, err := c.box.Run(ctx, elevation, "keep a volume for "+spec.Resource, volumeCreating(spec), nil); err != nil {
			return err
		}
	}
	delivery, err := c.hand(ctx, spec, secret)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, c.takeBack(ctx, delivery)) }()
	_, refused, failed := c.box.Attempt(ctx, elevation, "run "+spec.Resource+" as "+spec.Name, RunScript(spec, digest, delivery.Path), nil)
	if failed != nil && !strings.Contains(refused, nameTaken) {
		return failed
	}
	if _, err := c.box.Run(ctx, elevation, "wait for "+spec.Resource+" to answer", ReadyCommand(spec, shell.ReadySeconds), nil); err != nil {
		return err
	}
	if spec.Credential.Reassert != nil {
		argv, stdin := spec.Credential.Reassert(secret)
		if _, err := c.box.Run(ctx, elevation, "reassert "+spec.Resource+"'s credential", reassertCommand(spec.Name, argv), strings.NewReader(stdin)); err != nil {
			return err
		}
	}
	return nil
}

type Ref struct {
	Tier     environment.Tier
	Project  string
	Resource string
	Name     string
}

func (c *Containers) Remove(ctx context.Context, ref Ref) error {
	elevation, err := ssh.Docker, c.box.Assume(ctx, ssh.Docker)
	if err != nil {
		return err
	}
	volumes := volumesOf(ref.Tier, ref.Project, ref.Resource, ref.Name) + " --quiet"
	_, err = c.box.Run(ctx, elevation, "take "+ref.Name+" and its data down", shell.BoundedTolerating(shell.DockerStepSeconds, "docker rm of "+ref.Name,
		"docker rm --force "+shell.Quote(ref.Name)+" "+shell.Quote(ref.Name+RetiredSuffix)+" >/dev/null 2>&1")+"\n"+
		volumes+" | xargs -r "+shell.Within(shell.DockerStepSeconds, "docker volume rm")+" >/dev/null\n"+
		"[ -z \"$("+volumes+")\" ]", nil)
	if err != nil {
		return err
	}
	_, err = c.box.Run(ctx, elevation, "remove "+ref.Name+"'s kept credential and backups", "rm -f "+shell.Quote(KeptPath(ref.Tier, ref.Name))+"\nrm -rf "+shell.Quote(boxapi.BackupsDir(ref.Tier, ref.Name)), nil)
	return err
}

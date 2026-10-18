package host

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
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const (
	nameTaken = "is already in use"

	resourceStopSeconds = 60
	keptDir             = "kept"

	resourceNameMax = 63
)

func KeptPath(tier environment.Tier, name string) string {
	return StateDir(tier) + "/" + keptDir + "/" + name
}

func keptCommand(path string) string {
	return "if [ -e " + shell.Quote(stateRoot) + " ] && [ ! -x " + shell.Quote(stateRoot) + " ]; then echo " + shell.Quote(stateRoot+": Permission denied") + " >&2; exit 1; fi\n" +
		"if [ -s " + shell.Quote(path) + " ]; then cat " + shell.Quote(path) + "; fi"
}

func (h *Host) unhand(ctx context.Context, delivery handoff) error {
	taking, stop := context.WithTimeout(context.WithoutCancel(ctx), forgetWindow)
	defer stop()
	if _, err := h.owning(taking, "take back "+delivery.path, "rm -f "+shell.Quote(delivery.path), nil); err != nil {
		return refusal.Refuse(refusal.CodeNotReady,
			"could not remove %s on %s, which contains a plaintext credential: %v",
			delivery.path, h.Named(), err)
	}
	return nil
}

func handedBack(tier environment.Tier, path string) string {
	return "if [ \"$(id -u)\" -eq 0 ]; then chown -R --reference=" + shell.Quote(StateDir(tier)) + " " + shell.Quote(path) + "; fi"
}

func (h *Host) owning(ctx context.Context, what, command string, stdin []byte) (string, error) {
	var fed io.Reader
	if stdin != nil {
		fed = bytes.NewReader(stdin)
	}
	return h.Run(ctx, ssh.Deploy, what, command, fed)
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

func (h *Host) ForgetKept(ctx context.Context, tier environment.Tier, names []string) error {
	if len(names) == 0 {
		return nil
	}
	paths := make([]string, 0, len(names))
	for _, name := range names {
		paths = append(paths, shell.Quote(KeptPath(tier, name)))
	}
	_, err := h.owning(ctx, "forget what "+strings.Join(names, ", ")+" was bound to",
		"rm -f "+strings.Join(paths, " "), nil)
	return err
}

func (h *Host) Kept(ctx context.Context, tier environment.Tier, name string) ([]byte, error) {
	said, err := h.owning(ctx, "read what is kept for "+name, keptCommand(KeptPath(tier, name)), nil)
	if err != nil {
		return nil, err
	}
	return unkept(name, said)
}

func (h *Host) KeepOnce(ctx context.Context, tier environment.Tier, name string, candidate []byte) ([]byte, error) {
	fed := base64.StdEncoding.EncodeToString(candidate) + "\n"
	said, err := h.owning(ctx, "keep what "+name+" is bound to", keepCommand(tier, KeptPath(tier, name)), []byte(fed))
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

type ResourceContainer struct {
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

func (r ResourceContainer) socketsVolume() string { return r.Name + socketsSuffix }

func socketsCreating(spec ResourceContainer) string {
	if spec.Sockets == "" {
		return ""
	}
	return shell.Words(append(append([]string{"docker", "volume", "create"}, spec.labels()...), spec.socketsVolume())) + " >/dev/null\n"
}

func ResourceName(project, stack, resource, kind string) string {
	return naming.Fit(resourceNameMax, naming.WordSeparator,
		naming.Compressible(project), naming.Fixed(stack), naming.Fixed(resource), naming.Fixed(kind))
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
		"--label", docker.LabelTier + "=" + string(r.Tier),
		"--label", docker.LabelProject + "=" + naming.Sanitize(r.Project),
		"--label", docker.LabelResource + "=" + r.Resource,
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

func volumesOf(tier environment.Tier, project, resource, name string) string {
	return "docker volume ls --filter " + shell.Quote("name=^"+name) +
		" --filter " + shell.Quote("label="+docker.LabelTier+"="+string(tier)) +
		" --filter " + shell.Quote("label="+docker.LabelProject+"="+naming.Sanitize(project)) +
		" --filter " + shell.Quote("label="+docker.LabelResource+"="+resource)
}

func volumeCreating(spec ResourceContainer) string {
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

func runResourceScript(spec ResourceContainer, digest, envFile string) string {
	return docker.Pulled(spec.Image, appPulls, shell.PullAttemptSeconds, shell.PullBudgetSeconds) +
		socketsCreating(spec) +
		shell.Bounded(shell.DockerStepSeconds, "docker run of "+spec.Name, shell.Words(resourceRun(spec, digest, envFile))+" >/dev/null")
}

func resourceRun(spec ResourceContainer, digest, envFile string) []string {
	argv := []string{"docker", "run", "--detach",
		"--name", spec.Name,
		"--restart", appRestart,
		"--network", boxapi.AppNetwork(spec.Tier, spec.Project),
	}
	argv = append(argv, spec.labels()...)
	argv = append(argv, "--label", docker.LabelRef+"="+spec.Image, "--label", docker.LabelEnv+"="+digest)
	if spec.Backup != "" {
		argv = append(argv, "--label", LabelBackup+"="+spec.Backup)
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

func generationCommand(spec ResourceContainer) string {
	return volumesOf(spec.Tier, spec.Project, spec.Resource, spec.Name) +
		" --format " + shell.Quote(`{{.Label "`+docker.LabelGeneration+`"}}`)
}

func (h *Host) initialisedUnder(ctx context.Context, spec ResourceContainer, elevation ssh.Acting) (string, error) {
	if spec.Volume.Generation == "" {
		return "", nil
	}
	said, err := h.Run(ctx, elevation, "read what initialised "+spec.Resource+"'s data", generationCommand(spec), nil)
	if err != nil {
		return "", err
	}
	generations := strings.Fields(said)
	if len(generations) == 0 || slices.Contains(generations, spec.Volume.Generation) {
		return "", nil
	}
	return generations[0], nil
}

func swapCommand(spec ResourceContainer, from, digest, envFile string, steps upgradeSteps, secret string) (command, stdin string) {
	name, retired := shell.Quote(spec.Name), shell.Quote(spec.Name+retiredSuffix)
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
		"back() {\n" +
		"if [ \"$swapped\" -eq 0 ]; then\n" +
		"docker rm --force " + name + " >/dev/null 2>&1 || true\n" +
		dropFresh +
		"docker rename " + retired + " " + name + " >/dev/null 2>&1 || true\n" +
		"docker start " + name + " >/dev/null 2>&1 || true\n" +
		"fi\n" +
		"}\n" +
		"docker rm --force " + retired + " >/dev/null 2>&1 || true\n" +
		"trap back EXIT\n" +
		"trap 'exit 1' HUP INT TERM\n" +
		shell.Bounded(shell.DockerStepSeconds, "docker stop of "+spec.Name, "docker stop --time "+strconv.Itoa(resourceStopSeconds)+" "+name+" >/dev/null") + "\n" +
		"docker rename " + name + " " + retired + "\n" +
		createFresh +
		socketsCreating(spec) +
		steps.before +
		shell.Bounded(shell.DockerStepSeconds, "docker run of "+spec.Name, shell.Words(resourceRun(spec, digest, envFile))+" >/dev/null") + "\n" +
		readyCommand(spec, shell.ReadySeconds) + "\n" +
		authenticated +
		steps.after +
		"swapped=1\n" +
		"docker rm --force " + retired + " >/dev/null" +
		dropStale, stdin
}

func reassertCommand(name string, argv []string) string {
	return shell.Words(append([]string{"docker", "exec", "--interactive", name}, argv...)) + " >/dev/null 2>&1"
}

type upgradeSteps struct {
	before string
	after  string
}

func volumeCopy(spec ResourceContainer, from string) upgradeSteps {
	argv := []string{"docker", "run", "--rm", "--network", "none", "--cap-drop", "ALL"}
	if spec.User != "" {
		argv = append(argv, "--user", spec.User)
	}
	argv = append(argv,
		"--mount", "type=volume,src="+volumeName(spec.Name, from)+",dst=/from,readonly",
		"--mount", "type=volume,src="+spec.volume()+",dst="+spec.Volume.Path,
		"--entrypoint", "cp", spec.Image, "-a", "/from/.", spec.Volume.Path+"/")
	return upgradeSteps{before: shell.Words(argv) + "\n"}
}

func (h *Host) prepareUpgrade(ctx context.Context, spec ResourceContainer, from string, elevation ssh.Acting) (upgradeSteps, error) {
	if spec.Volume.CopyOnUpgrade {
		return volumeCopy(spec, from), nil
	}
	dumped, err := h.Run(ctx, elevation, "dump "+spec.Resource+" while version "+from+" still serves it", dumpCommand(spec.Tier, spec.Name), nil)
	if err != nil {
		return upgradeSteps{}, err
	}
	return upgradeSteps{after: restoreCommand(spec.Tier, spec.Name, strings.TrimSpace(dumped)) + "\n"}, nil
}

func (h *Host) upgrade(ctx context.Context, spec ResourceContainer, from, digest, secret string, elevation ssh.Acting) (err error) {
	steps, err := h.prepareUpgrade(ctx, spec, from, elevation)
	if err != nil {
		return err
	}
	delivery, err := h.handResource(ctx, spec, secret)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, h.unhand(ctx, delivery)) }()
	swap, stdin := swapCommand(spec, from, digest, delivery.path, steps, secret)
	if _, err := h.Run(ctx, elevation, "move "+spec.Resource+" from version "+from+" to "+spec.Volume.Generation, swap, strings.NewReader(stdin)); err != nil {
		return refusal.Refuse(refusal.CodeNotReady,
			"%s could not move from version %s to %s and is back on %s, data intact: %v",
			spec.Resource, from, spec.Volume.Generation, from, err)
	}
	return nil
}

func (h *Host) limited(ctx context.Context, spec ResourceContainer) (ResourceContainer, error) {
	limits, err := h.engine.Limits(ctx, spec.Tier)
	if err != nil {
		return ResourceContainer{}, err
	}
	if spec.Memory == "" {
		spec.Memory = strconv.FormatInt(limits.MemoryBytes, 10)
	}
	if spec.CPUs == "" {
		spec.CPUs = limits.CPUs
	}
	return spec, nil
}

func (h *Host) replaceResource(ctx context.Context, spec ResourceContainer, digest, secret string, elevation ssh.Acting) (err error) {
	if err := h.sweep(ctx, elevation); err != nil {
		return err
	}
	delivery, err := h.handResource(ctx, spec, secret)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, h.unhand(ctx, delivery)) }()
	swap, stdin := swapCommand(spec, spec.Volume.Generation, digest, delivery.path, upgradeSteps{}, secret)
	if _, err := h.Run(ctx, elevation, "replace "+spec.Resource+"'s container "+spec.Name, swap, strings.NewReader(stdin)); err != nil {
		return refusal.Refuse(refusal.CodeNotReady,
			"%s's new container did not take over, so its previous container is back, data intact: %v", spec.Resource, err)
	}
	return nil
}

func (h *Host) handResource(ctx context.Context, spec ResourceContainer, secret string) (handoff, error) {
	env := maps.Clone(spec.Env)
	if env == nil {
		env = map[string]string{}
	}
	if spec.Credential.Env != "" {
		env[spec.Credential.Env] = secret
	}
	rendered, err := RenderEnvFile(env)
	if err != nil {
		return handoff{}, err
	}
	delivery := handoff{path: EnvFile(spec.Tier, spec.Name)}
	_, err = h.owning(ctx, "write what "+spec.Resource+" is handed",
		renderPipedInstall("-m 0600", delivery.path), rendered)
	return delivery, err
}

func readyCommand(spec ResourceContainer, seconds int) string {
	probe := shell.Within(min(shell.ProbeSeconds, seconds), shell.Words(append([]string{"docker", "exec", spec.Name}, spec.Ready...)))
	return "deadline=$(($(date +%s) + " + strconv.Itoa(seconds) + "))\n" +
		"until " + probe + " >/dev/null 2>&1; do\n" +
		"if [ \"$(date +%s)\" -ge \"$deadline\" ]; then\n" +
		"printf '%s\\n' " + shell.Quote(spec.Name+" never answered "+strings.Join(spec.Ready, " ")+" within "+shell.Seconds(seconds)) + " >&2\n" +
		shell.Within(shell.ProbeSeconds, "docker logs --tail "+appLogTail+" "+shell.Quote(spec.Name)) + " >&2 || true\n" +
		"exit 1\n" +
		"fi\n" +
		"sleep 1\n" +
		"done"
}

type resourceReading struct {
	spec    ResourceContainer
	digest  string
	serving string
	from    string
}

func (h *Host) readResource(ctx context.Context, spec ResourceContainer, elevation ssh.Acting) (resourceReading, error) {
	spec, err := h.limited(ctx, spec)
	if err != nil {
		return resourceReading{}, err
	}
	digest, err := spec.digest()
	if err != nil {
		return resourceReading{}, err
	}
	said, err := h.engine.ReadServing(ctx, spec.Name)
	if err != nil {
		return resourceReading{}, err
	}
	read := resourceReading{spec: spec, digest: digest, serving: said}
	if read.isCurrent() {
		return read, nil
	}
	if read.from, err = h.initialisedUnder(ctx, spec, elevation); err != nil {
		return resourceReading{}, err
	}
	return read, read.refuseUpgradeOverStoppedServer()
}

func (r resourceReading) isCurrent() bool { return docker.IsServing(r.serving, r.spec.Image, r.digest) }

func (r resourceReading) refuseUpgradeOverStoppedServer() error {
	if r.from == "" || r.spec.Volume.CopyOnUpgrade || strings.HasPrefix(r.serving, "running ") {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"%s has version %s data, this deploy declares %s, and %s is not running to dump it\n"+
			"Run `docker start %s` or set the version back to %s",
		r.spec.Resource, r.from, r.spec.Volume.Generation, r.spec.Name, r.spec.Name, r.from)
}

func (r resourceReading) planChange() (provider.ChangeAction, string) {
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

func (h *Host) PlanResource(ctx context.Context, spec ResourceContainer) (provider.ChangeAction, string, error) {
	elevation, err := ssh.Docker, h.Assume(ctx, ssh.Docker)
	if err != nil {
		return "", "", err
	}
	read, err := h.readResource(ctx, spec, elevation)
	if err != nil {
		return "", "", err
	}
	action, reason := read.planChange()
	return action, reason, nil
}

func (h *Host) RunResource(ctx context.Context, spec ResourceContainer, secret string) (err error) {
	elevation, err := ssh.Docker, h.Assume(ctx, ssh.Docker)
	if err != nil {
		return err
	}
	if err := h.engine.Join(ctx, spec.Resource, spec.Tier, spec.Project); err != nil {
		return err
	}
	read, err := h.readResource(ctx, spec, elevation)
	if err != nil {
		return err
	}
	spec, digest := read.spec, read.digest
	switch {
	case read.isCurrent():
		return nil
	case read.from != "":
		return h.upgrade(ctx, spec, read.from, digest, secret, elevation)
	case read.serving != "":
		return h.replaceResource(ctx, spec, digest, secret, elevation)
	}
	if err := h.sweep(ctx, elevation); err != nil {
		return err
	}
	if spec.Volume.Path != "" {
		if _, err := h.Run(ctx, elevation, "keep a volume for "+spec.Resource, volumeCreating(spec), nil); err != nil {
			return err
		}
	}
	delivery, err := h.handResource(ctx, spec, secret)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, h.unhand(ctx, delivery)) }()
	_, refused, failed := h.Attempt(ctx, elevation, "run "+spec.Resource+" as "+spec.Name, runResourceScript(spec, digest, delivery.path), nil)
	if failed != nil && !strings.Contains(refused, nameTaken) {
		return failed
	}
	if _, err := h.Run(ctx, elevation, "wait for "+spec.Resource+" to answer", readyCommand(spec, shell.ReadySeconds), nil); err != nil {
		return err
	}
	if spec.Credential.Reassert != nil {
		argv, stdin := spec.Credential.Reassert(secret)
		if _, err := h.Run(ctx, elevation, "reassert "+spec.Resource+"'s credential", reassertCommand(spec.Name, argv), strings.NewReader(stdin)); err != nil {
			return err
		}
	}
	return nil
}

type ResourceRef struct {
	Tier     environment.Tier
	Project  string
	Resource string
	Name     string
}

func (h *Host) RemoveResource(ctx context.Context, ref ResourceRef) error {
	elevation, err := ssh.Docker, h.Assume(ctx, ssh.Docker)
	if err != nil {
		return err
	}
	volumes := volumesOf(ref.Tier, ref.Project, ref.Resource, ref.Name) + " --quiet"
	_, err = h.Run(ctx, elevation, "take "+ref.Name+" and its data down", shell.BoundedTolerating(shell.DockerStepSeconds, "docker rm of "+ref.Name,
		"docker rm --force "+shell.Quote(ref.Name)+" "+shell.Quote(ref.Name+retiredSuffix)+" >/dev/null 2>&1")+"\n"+
		volumes+" | xargs -r "+shell.Within(shell.DockerStepSeconds, "docker volume rm")+" >/dev/null\n"+
		"[ -z \"$("+volumes+")\" ]", nil)
	if err != nil {
		return err
	}
	_, err = h.Run(ctx, elevation, "remove "+ref.Name+"'s kept credential and backups", "rm -f "+shell.Quote(KeptPath(ref.Tier, ref.Name))+"\nrm -rf "+shell.Quote(BackupsDir(ref.Tier, ref.Name)), nil)
	return err
}

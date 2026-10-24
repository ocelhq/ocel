package release

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/envfile"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const (
	liveDirTmpfs = "rw,noexec,nosuid,size=8m"
)

var AppCapabilities = []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL", "NET_BIND_SERVICE", "SETGID", "SETUID", "SETPCAP"}

type Container struct {
	Name    string
	Project string
	App     string
	Image   string
	Tier    environment.Tier

	Env        map[string]string
	HealthPath string
	Manifest   []byte
	Resolved   bool
	Declared   []string

	Limits docker.Limits
}

type Containers struct {
	box     *ssh.Box
	engine  *docker.Engine
	routing *routing.Table
	front   proxy.Proxy
	images  *Images
}

func NewContainers(box *ssh.Box, engine *docker.Engine, routing *routing.Table, front proxy.Proxy, images *Images) *Containers {
	return &Containers{box: box, engine: engine, routing: routing, front: front, images: images}
}

func (c Container) delivered() map[string]string {
	env := make(map[string]string, len(c.Env)+2)
	maps.Copy(env, c.Env)
	if c.HealthPath != "" {
		env[originguard.HealthPathVar] = c.HealthPath
	}
	if len(c.Manifest) > 0 {
		env[boxapi.EnvVar] = string(c.Manifest)
	}
	return env
}

func ContainerName(project string, stack naming.StackName, worker, image string) (string, error) {
	tag := stack.Release.String()
	if stack.Release.IsZero() {
		tag = naming.HashTag(project, stack.Env, stack.App, worker, image)
	}
	return naming.PhysicalName{Project: project, Env: stack.Env, App: stack.App, Declared: worker, Tag: tag}.Fit(docker.NameMax)
}

func RunContainerScript(spec Container, env envfile.Delivery) string {
	return docker.Pulled(spec.Image, docker.PullAttempts, shell.PullAttemptSeconds, shell.PullBudgetSeconds) +
		shell.Bounded(shell.DockerStepSeconds, "docker run of "+spec.Name, shell.Words(ContainerRun(spec, env))+" >/dev/null")
}

func ContainerRun(spec Container, env envfile.Delivery) []string {
	argv := []string{"docker", "run", "--detach",
		"--name", spec.Name,
		"--restart", docker.RestartPolicy,
		"--init=false",
		"--network", boxapi.AppNetwork(spec.Tier, spec.Project),
		"--label", docker.LabelTier + "=" + string(spec.Tier),
		"--label", docker.LabelProject + "=" + naming.Sanitize(spec.Project),
		"--label", docker.LabelApp + "=" + spec.App,
		"--label", docker.LabelRef + "=" + spec.Image,
		"--label", docker.LabelEnv + "=" + env.Digest,
	}
	argv = append(argv, docker.Logging()...)
	argv = append(argv, docker.Confined(AppCapabilities, false)...)
	argv = append(argv, spec.Limits.Argv()...)
	if env.Path != "" {
		argv = append(argv, "--env-file", env.Path)
	}
	if len(spec.Manifest) > 0 {
		argv = append(argv, "--mount", "type=bind,src="+boxapi.SocketDir+",dst="+boxapi.SocketDir+",readonly",
			"--tmpfs", containerimage.LivePath+":"+liveDirTmpfs)
	}
	return append(argv, "--env", containerimage.PortEnvVar+"="+containerimage.PortText, spec.Image)
}

type Run uint8

const (
	RunKept Run = iota
	RunCreated
	RunRestarted
)

func (c *Containers) Run(ctx context.Context, spec Container) (ran Run, err error) {
	elevation, err := ssh.Docker, c.box.Assume(ctx, ssh.Docker)
	if err != nil {
		return RunKept, err
	}
	if spec.Limits, err = c.engine.Limits(ctx, spec.Tier); err != nil {
		return RunKept, err
	}
	if err := c.engine.Join(ctx, spec.App, spec.Tier, spec.Project); err != nil {
		return RunKept, err
	}
	said, err := c.engine.ReadServing(ctx, spec.Name)
	if err != nil {
		return RunKept, err
	}
	if spec.Resolved {
		env, err := Handing(spec)
		if err != nil {
			return RunKept, err
		}
		if docker.IsServing(said, spec.Image, env.Digest) {
			return RunKept, nil
		}
	} else {
		if docker.IsRunningImage(said, spec.Image) {
			return RunKept, nil
		}
		if docker.IsStartableImage(said, spec.Image) {
			_, err := c.box.Run(ctx, elevation, "start "+spec.App+" back up as "+spec.Name, docker.RestartCommand(said, spec.Name), nil)
			return RunRestarted, err
		}
		note, known, err := c.handed(ctx, spec)
		if err != nil {
			return RunKept, err
		}
		switch {
		case known && len(note.Handed) > 0:
			return RunKept, refusal.Refuse(refusal.CodeNotReady,
				"%s is gone from this box and was handed %s, which a promotion cannot pass on\nRun `ocel deploy`",
				spec.Name, strings.Join(note.Handed, ", "))
		case known:
			spec.Manifest = note.Live
		case len(spec.Declared) > 0:
			return RunKept, refusal.Refuse(refusal.CodeNotReady,
				"%s is gone from this box, %s declares %s, and no note of its values remains\nRun `ocel deploy`",
				spec.Name, spec.App, strings.Join(spec.Declared, ", "))
		default:
			return RunKept, refusal.Refuse(refusal.CodeNotReady,
				"%s is gone from this box and no note of its values remains\nRun `ocel deploy`",
				spec.Name)
		}
	}
	env, err := Handing(spec)
	if err != nil {
		return RunKept, err
	}
	if _, err := c.box.Run(ctx, elevation, "clear the name "+spec.Name, "docker rm --force "+shell.Quote(spec.Name)+" >/dev/null 2>&1 || true", nil); err != nil {
		return RunCreated, err
	}
	if err := envfile.Sweep(ctx, c.box, elevation); err != nil {
		return RunCreated, err
	}
	defer func() { err = errors.Join(err, c.forget(ctx, env)) }()
	if err := c.hand(ctx, env, spec); err != nil {
		return RunCreated, err
	}
	if spec.Resolved {
		if err := c.note(ctx, spec); err != nil {
			return RunCreated, err
		}
	}
	_, err = c.box.Run(ctx, elevation, "run "+spec.App+" as "+spec.Name, RunContainerScript(spec, env), nil)
	return RunCreated, err
}

func (c *Containers) TakeDown(ctx context.Context, tier environment.Tier, name string) error {
	elevation, err := ssh.Docker, c.box.Assume(ctx, ssh.Docker)
	if err != nil {
		return err
	}
	_, err = c.box.Run(ctx, elevation, "take down "+name, shell.Within(shell.DockerStepSeconds, "docker stop "+shell.Quote(name))+" >/dev/null 2>&1 || true\n"+
		shell.BoundedTolerating(shell.DockerStepSeconds, "docker rm of "+name, "docker rm --force "+shell.Quote(name)+" >/dev/null 2>&1"), nil)
	if err != nil {
		return err
	}
	acting, err := ssh.Deploy, c.box.Assume(ctx, ssh.Deploy)
	if err != nil {
		return err
	}
	_, err = c.box.Run(ctx, acting, "forget what "+name+" was handed", "rm -f "+shell.Quote(HandedNote(tier, name)), nil)
	return err
}

func (c *Containers) Stop(ctx context.Context, name string) error {
	elevation, err := ssh.Docker, c.box.Assume(ctx, ssh.Docker)
	if err != nil {
		return err
	}
	_, err = c.box.Run(ctx, elevation, "stop "+name, shell.BoundedTolerating(shell.DockerStepSeconds, "docker stop of "+name, "docker stop "+shell.Quote(name)+" >/dev/null 2>&1"), nil)
	return err
}

func ReleaseSelectors() string {
	return docker.LabelSelector(docker.LabelProject) + " " + docker.LabelSelector(docker.LabelApp) + " " + docker.LabelSelector(docker.LabelRef)
}

func ReleaseCommand(name string) string {
	return "docker inspect --type container --format " + shell.Quote(ReleaseSelectors()) + " " + shell.Quote(name)
}

func (c *Containers) Retire(ctx context.Context, name string) error {
	elevation, err := ssh.Docker, c.box.Assume(ctx, ssh.Docker)
	if err != nil {
		return err
	}
	said, err := c.box.Run(ctx, elevation, "read which release "+name+" runs", ReleaseCommand(name), nil)
	if err != nil {
		return err
	}
	release := strings.Fields(said)
	if len(release) != 3 {
		return refusal.Refuse(refusal.CodeNotReady,
			"%s lacks the %s, %s or %s label every app container carries, so its image cannot be archived for a rollback: %q",
			name, docker.LabelProject, docker.LabelApp, docker.LabelRef, strings.TrimSpace(said))
	}
	if err := c.images.archive(ctx, release[0], release[1], release[2]); err != nil {
		return err
	}
	_, err = c.box.Run(ctx, elevation, "stop "+name, shell.Bounded(shell.DockerStepSeconds, "docker stop of "+name, "docker stop "+shell.Quote(name)+" >/dev/null"), nil)
	return err
}

func (c *Containers) Remove(ctx context.Context, name string) error {
	elevation, err := ssh.Docker, c.box.Assume(ctx, ssh.Docker)
	if err != nil {
		return err
	}
	_, err = c.box.Run(ctx, elevation, "remove "+name, shell.Bounded(shell.DockerStepSeconds, "docker rm of "+name, "docker rm --force "+shell.Quote(name)+" >/dev/null"), nil)
	return err
}

func LogCommand(name string) string {
	return "docker logs --timestamps --tail " + docker.LogTail + " " + shell.Quote(name) + " 2>&1 || true"
}

var ErrContainerMissing = errors.New("the container does not exist")

type Line struct {
	Time   time.Time
	Stderr bool
	Text   string
}

func (c *Containers) ReadLogs(ctx context.Context, name string, since, until time.Time, limit int, contains string) ([]Line, error) {
	elevation, err := ssh.Docker, c.box.Assume(ctx, ssh.Docker)
	if err != nil {
		return nil, err
	}
	result, err := c.box.Stream(ctx, elevation, logsCommand(name, since, until, limit, contains), nil)
	if err != nil {
		return nil, err
	}
	if result.Code != 0 {
		if strings.Contains(strings.ToLower(result.Stderr), "no such container") {
			return nil, ErrContainerMissing
		}
		return nil, c.box.Refuse("read the logs of "+name, result)
	}
	lines := slices.DeleteFunc(append(parseLogLines(result.Stdout, false), parseLogLines(result.Stderr, true)...), func(line Line) bool {
		return line.Time.Before(since) || !until.IsZero() && line.Time.After(until) || !strings.Contains(line.Text, contains)
	})
	slices.SortStableFunc(lines, func(a, b Line) int { return a.Time.Compare(b.Time) })
	return lines[len(lines)-min(len(lines), max(limit, 0)):], nil
}

func logsCommand(name string, since, until time.Time, limit int, contains string) string {
	command := "docker logs --timestamps --since " + shell.Quote(since.UTC().Format(time.RFC3339Nano))
	if !until.IsZero() {
		command += " --until " + shell.Quote(until.UTC().Format(time.RFC3339Nano))
	}
	if until.IsZero() && contains == "" {
		command += " --tail " + strconv.Itoa(limit)
	}
	return command + " " + shell.Quote(name)
}

func parseLogLines(output string, stderr bool) []Line {
	var lines []Line
	for raw := range strings.SplitSeq(output, "\n") {
		if line, stamped := parseLogLine(raw, stderr); stamped {
			lines = append(lines, line)
		}
	}
	return lines
}

func parseLogLine(raw string, stderr bool) (Line, bool) {
	stamp, text, _ := strings.Cut(strings.TrimSuffix(raw, "\r"), " ")
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return Line{}, false
	}
	return Line{Time: at.UTC(), Stderr: stderr, Text: text}, true
}

func (c *Containers) ForgetNetwork(ctx context.Context, tier environment.Tier, project string) error {
	return c.engine.ForgetNetwork(ctx, tier, project)
}

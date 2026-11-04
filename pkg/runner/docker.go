package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/tracing"
)

const dockerLabel = "dev.ocel.runner.job"

type Docker struct {
	Image        string
	RunnerURL    string
	Network      string
	DockerSocket string
	CPUs         string
	Memory       string
	Mounts       []string
	Env          []string
	Now          func() time.Time
	Run          func(ctx context.Context, env []string, args ...string) (string, error)
}

func (d *Docker) Launch(ctx context.Context, launch Launch) (string, error) {
	if d.Image == "" || d.RunnerURL == "" {
		return "", errors.New("runner: the Docker launcher needs the job image and the URL its agent reaches the runner at")
	}
	if !jobIDPattern(launch.JobID) {
		return "", fmt.Errorf("runner: job id %q is not one a container can be named after", launch.JobID)
	}
	name := ContainerName(launch.JobID)
	if _, err := d.docker(ctx, nil, "container", "inspect", "--format", "{{.Id}}", name); err == nil {
		return name, nil
	}
	if _, err := d.docker(ctx, []string{TokenEnvVar + "=" + launch.Token}, d.RunArgs(launch)...); err != nil {
		if _, inspect := d.docker(ctx, nil, "container", "inspect", "--format", "{{.Id}}", name); inspect == nil {
			return name, nil
		}
		return "", fmt.Errorf("runner: start job %s's agent: %w", launch.JobID, err)
	}
	return name, nil
}

func ContainerName(jobID string) string { return "ocel-job-" + jobID }

func (d *Docker) RunArgs(launch Launch) []string {
	args := []string{"run", "--detach", "--rm", "--name", ContainerName(launch.JobID),
		"--label", dockerLabel + "=" + launch.JobID,
		"--env", TokenEnvVar,
	}
	if d.Network != "" {
		args = append(args, "--network", d.Network)
	}
	if d.DockerSocket != "" {
		args = append(args, "--mount", "type=bind,source="+d.DockerSocket+",target=/var/run/docker.sock",
			"--env", "DOCKER_HOST=unix:///var/run/docker.sock")
	}
	if d.CPUs != "" {
		args = append(args, "--cpus", d.CPUs)
	}
	if d.Memory != "" {
		args = append(args, "--memory", d.Memory)
	}
	for _, mount := range d.Mounts {
		args = append(args, "--mount", mount)
	}
	for _, kv := range d.Env {
		args = append(args, "--env", kv)
	}
	args = append(args, d.Image, "ocel-job", "--runner", d.RunnerURL)
	if !launch.Deadline.IsZero() {
		now := time.Now
		if d.Now != nil {
			now = d.Now
		}
		args = append(args, "--timeout", max(launch.Deadline.Sub(now()), time.Second).Round(time.Second).String())
	}
	return args
}

func (d *Docker) Cancel(ctx context.Context, handle string) error {
	if handle == "" {
		return nil
	}
	_, err := d.docker(ctx, nil, "container", "rm", "--force", handle)
	if err != nil && strings.Contains(err.Error(), "No such container") {
		return nil
	}
	return err
}

func (d *Docker) docker(ctx context.Context, env []string, args ...string) (string, error) {
	if d.Run != nil {
		return d.Run(ctx, env, args...)
	}
	cmd := tracing.CommandContext(ctx, "docker", args...)
	cmd.Env = append(os.Environ(), env...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

package jobcontext

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/githubactions"
	"github.com/ocelhq/ocel/cli/internal/jobinput"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
	"github.com/ocelhq/ocel/pkg/runner"
)

type Credential interface {
	Token(ctx context.Context, audience string) (string, error)
}

type JobToken struct {
	File string
}

func (t JobToken) Token(context.Context, string) (string, error) {
	read, err := os.ReadFile(t.File)
	if err != nil {
		return "", fmt.Errorf("read the job token: %w", err)
	}
	token := strings.TrimSpace(string(read))
	if token == "" {
		return "", fmt.Errorf("the job token file %s is empty", t.File)
	}
	return token, nil
}

type GitHubActions struct {
	Getenv func(string) string
}

func (g GitHubActions) Token(ctx context.Context, audience string) (string, error) {
	return githubactions.RequestIDToken(ctx, g.Getenv, audience)
}

type Context struct {
	ConsoleURL string
	ProjectID  string
	Credential Credential
	Job        *Job
}

type Job struct {
	ID           string
	DeploymentID string
	Slug         string
	PreviewLabel string
	Tier         environmentv1.Tier
	Git          *runnerv1.GitContext
	Input        *jobinput.Answers
}

func Read(getenv func(string) string) (*Context, error) {
	if path := getenv(runner.ContextEnvVar); path != "" {
		return readJob(path)
	}
	if githubactions.IsRunning(getenv) && getenv(console.ProjectIDEnvVar) != "" {
		return &Context{
			ConsoleURL: console.NormalizeBaseURL(getenv(console.URLEnvVar)),
			ProjectID:  getenv(console.ProjectIDEnvVar),
			Credential: GitHubActions{Getenv: getenv},
		}, nil
	}
	return nil, nil
}

func readJob(path string) (*Context, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, refuse(fmt.Errorf("%s is set, so this runs inside a job, and its context could not be read: %w", runner.ContextEnvVar, err))
	}
	var read runnerv1.JobContext
	if err := protojson.Unmarshal(encoded, &read); err != nil {
		return nil, refuse(fmt.Errorf("%s, named by %s, is not a job's context: %w", path, runner.ContextEnvVar, err))
	}
	if err := protovalidate.Validate(&read); err != nil {
		return nil, refuse(fmt.Errorf("%s, named by %s, holds a job's context that is not valid: %w", path, runner.ContextEnvVar, err))
	}
	return &Context{
		ConsoleURL: console.NormalizeBaseURL(read.GetConsoleUrl()),
		ProjectID:  read.GetProjectId(),
		Credential: JobToken{File: read.GetJobTokenFile()},
		Job: &Job{
			ID:           read.GetJobId(),
			DeploymentID: read.GetDeploymentId(),
			Slug:         read.GetSlug(),
			PreviewLabel: read.GetPreviewLabel(),
			Tier:         read.GetTier(),
			Git:          read.GetGit(),
		},
	}, nil
}

func refuse(err error) error {
	return &clierror.Error{Code: clierror.CodeJobContextInvalid, Cause: err}
}

func (c *Context) JobToken() (JobToken, bool) {
	token, isJob := c.Credential.(JobToken)
	return token, isJob && c.Job != nil
}

func (c *Context) Bearer(ctx context.Context) (string, error) {
	audience, err := console.Origin(c.ConsoleURL)
	if err != nil {
		return "", err
	}
	return c.Credential.Token(ctx, audience)
}

func (j *Job) Source() *consolev1.Source {
	if j.Git.GetCommit() == "" {
		return nil
	}
	return &consolev1.Source{Commit: j.Git.GetCommit(), Branch: j.Git.GetBranch(), Dirty: j.Git.GetDirty()}
}

func (j *Job) ReadGitBranch(context.Context, string) (string, error) {
	if j.Git.GetBranch() == "" {
		return "", fmt.Errorf("job %s was started with no git branch, so no branch names this preview: name it, as in `ocel preview up <name>`", j.ID)
	}
	return j.Git.GetBranch(), nil
}

func (j *Job) DiscoverPRNumber() string {
	if j.Git.GetPullRequest() == 0 {
		return ""
	}
	return strconv.FormatUint(uint64(j.Git.GetPullRequest()), 10)
}

func (t JobToken) ReadSubject(ctx context.Context) (string, error) {
	token, err := t.Token(ctx, "")
	if err != nil {
		return "", err
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("the job token in %s is not a JWT", t.File)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("the job token in %s has a payload that is not base64url: %w", t.File, err)
	}
	var claims struct {
		Subject string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Subject == "" {
		return "", fmt.Errorf("the job token in %s names no subject", t.File)
	}
	return claims.Subject, nil
}

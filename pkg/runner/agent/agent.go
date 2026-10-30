package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"buf.build/go/protovalidate"
	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/naming"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
	"github.com/ocelhq/ocel/pkg/proto/runner/v1/runnerv1connect"
	"github.com/ocelhq/ocel/pkg/runner"
	"github.com/ocelhq/ocel/pkg/tracing"
)

const (
	jobContextFile = "job-context.json"
	jobTokenFile   = "job-token"
	buildIDFile    = "build-id"
	maxOutputBytes = 4 << 20
	giveUpAfter    = 5 * time.Minute
	stepTail       = 5
	maxLine        = 16 << 20
)

type Config struct {
	Runner       string
	Token        string
	Ocel         string
	Git          string
	Dir          string
	Timeout      time.Duration
	Env          []string
	HTTP         *http.Client
	ReportEvery  time.Duration
	BeatEvery    time.Duration
	StopGrace    time.Duration
	FindInstalls func(ctx context.Context, source, project string) ([]Install, error)
}

type Install struct {
	Dir  string
	Args []string
}

var errJobOver = errors.New("the runner ended the job")

func Run(ctx context.Context, config Config) error {
	if config.Runner == "" || config.Token == "" || config.Ocel == "" || config.Dir == "" {
		return errors.New("agent: the agent needs the runner's URL, the job token, the ocel binary and a directory")
	}
	if config.HTTP == nil {
		config.HTTP = tracing.NewClient(0)
	}
	if config.Git == "" {
		config.Git = "git"
	}
	config.ReportEvery = orDefault(config.ReportEvery, time.Second)
	config.BeatEvery = orDefault(config.BeatEvery, 10*time.Second)
	config.StopGrace = orDefault(config.StopGrace, 30*time.Second)
	client := runnerv1connect.NewJobAgentServiceClient(config.HTTP, strings.TrimRight(config.Runner, "/"), connect.WithInterceptors(bearer(config.Token)))
	a := &agent{config: config, client: client}

	spec, err := retry(ctx, func(ctx context.Context) (*runnerv1.GetSpecResponse, error) {
		return client.GetSpec(ctx, &runnerv1.GetSpecRequest{})
	})
	if errors.Is(err, errJobOver) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("agent: read the job's spec: %w", err)
	}
	return a.run(ctx, spec)
}

type agent struct {
	config Config
	client runnerv1connect.JobAgentServiceClient
}

func (a *agent) run(ctx context.Context, spec *runnerv1.GetSpecResponse) error {
	dir := a.config.Dir
	source, home := filepath.Join(dir, "source"), filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		return a.fail(ctx, runner.CodeAgent, err)
	}
	tokenFile := filepath.Join(dir, jobTokenFile)
	if err := os.WriteFile(tokenFile, []byte(a.config.Token), 0o600); err != nil {
		return a.fail(ctx, runner.CodeAgent, err)
	}
	if err := writeJobContext(dir, spec, tokenFile); err != nil {
		return a.fail(ctx, runner.CodeAgent, err)
	}
	args := Args(spec.GetJob(), spec.GetTier())
	if len(args) == 0 {
		return a.fail(ctx, runner.CodeAgent, fmt.Errorf("the job runs no ocel command: %v", spec.GetJob().GetCommand()))
	}

	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	if a.config.Timeout > 0 {
		runCtx, stop = context.WithTimeout(runCtx, a.config.Timeout)
		defer stop()
	}
	out := newOutput(a.client)
	reported := make(chan struct{})
	go func() {
		defer close(reported)
		out.loop(runCtx, a.config.ReportEvery, a.config.BeatEvery, stop)
	}()
	finish := a.work(runCtx, out, source, home, dir, spec, args)
	timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)
	stop()
	<-reported
	if out.over() {
		return nil
	}
	if err := out.drain(ctx); err != nil {
		return a.lost(err)
	}
	if timedOut {
		finish.Error, finish.ErrorCode = fmt.Sprintf("the job ran past %s and was stopped", a.config.Timeout), runner.CodeTimeout
	}
	if finish.GetExitCode() == 0 && finish.GetError() == "" {
		finish.Builds = buildIDs(source, spec.GetJob().GetProjectPath())
	}
	return a.finish(ctx, finish)
}

func (a *agent) work(ctx context.Context, out *output, source, home, dir string, spec *runnerv1.GetSpecResponse, args []string) *runnerv1.FinishRequest {
	early := func(message string) *runnerv1.FinishRequest {
		return &runnerv1.FinishRequest{ExitCode: -1, Error: message, ErrorCode: runner.CodeSource}
	}
	if err := a.materialise(ctx, out, source, home, spec); err != nil {
		return early(err.Error())
	}
	project := source
	if p := spec.GetJob().GetProjectPath(); p != "" {
		if err := checkPath(p); err != nil {
			return early(err.Error())
		}
		project = filepath.Join(source, filepath.FromSlash(p))
	}
	if info, err := os.Lstat(project); err != nil || !info.IsDir() {
		return early(fmt.Sprintf("the source has no directory %s, where the job was started for the project", spec.GetJob().GetProjectPath()))
	}
	if c := spec.GetJob().GetConfigFile(); c != "" && checkPath(c) != nil {
		return early(fmt.Sprintf("the job's config file %q is not a relative path inside the project", c))
	}
	return a.steps(ctx, out, source, project, a.installEnv(home), a.runEnv(dir, home), args)
}

func (a *agent) materialise(ctx context.Context, out *output, source, home string, spec *runnerv1.GetSpecResponse) error {
	if git := spec.GetJob().GetGitSource(); git != nil {
		out.say("Cloning the source at " + git.GetCommit())
		return clone(ctx, a.config.Git, source, home, git)
	}
	archive := spec.GetArchive()
	out.say(fmt.Sprintf("Unpacking the source: %d files", len(archive.GetManifest().GetFiles())))
	urls := map[string]string{}
	for _, blob := range archive.GetBlobs() {
		urls[blob.GetDigest()] = blob.GetUrl()
	}
	if err := Materialise(ctx, source, archive.GetManifest(), a.blobs(urls)); err != nil {
		return fmt.Errorf("unpack the source: %w", err)
	}
	return nil
}

func (a *agent) steps(ctx context.Context, out *output, source, project string, installEnv, runEnv, args []string) *runnerv1.FinishRequest {
	var missing []Install
	var err error
	if a.config.FindInstalls != nil {
		missing, err = a.config.FindInstalls(ctx, source, project)
	}
	if err != nil {
		return &runnerv1.FinishRequest{ExitCode: -1, Error: err.Error(), ErrorCode: runner.CodeSource}
	}
	for _, each := range missing {
		if failed := a.install(ctx, out, source, each, installEnv); failed != nil {
			return failed
		}
	}
	stream := &runner.Stream{}
	end := a.step(ctx, project, runEnv, append([]string{a.config.Ocel}, args...), func(line []byte) {
		out.line(string(line))
		if progress, ok := stream.Line(line); ok && progress.Input != nil {
			out.asked()
		}
	}, func(line []byte) { out.errLine(string(line)) }, func(ctx context.Context, stdin io.Writer) { a.answer(ctx, out, stdin) })
	finish := &runnerv1.FinishRequest{ExitCode: int32(end.code)}
	if end.err != nil {
		finish.Error, finish.ErrorCode = end.why(), runner.CodeRun
	}
	return finish
}

func (a *agent) install(ctx context.Context, out *output, source string, missing Install, env []string) *runnerv1.FinishRequest {
	what := fmt.Sprintf("the dependencies in %s with %s", relative(source, missing.Dir), strings.Join(missing.Args, " "))
	out.say("Installing " + what)
	failed := func(end ended) *runnerv1.FinishRequest {
		return &runnerv1.FinishRequest{ExitCode: int32(end.code), Error: fmt.Sprintf("install %s: %s", what, end.why()), ErrorCode: runner.CodeRun}
	}
	manager, err := lookPath(missing.Args[0], env)
	if err != nil {
		return failed(ended{code: -1, err: err})
	}
	end := a.step(ctx, missing.Dir, env, append([]string{manager}, missing.Args[1:]...), out.output(progressv1.Stream_STREAM_STDOUT), out.output(progressv1.Stream_STREAM_STDERR), nil)
	if end.failed() {
		return failed(end)
	}
	return nil
}

type ended struct {
	code   int
	stderr []string
	err    error
}

func (e ended) failed() bool { return e.code != 0 || e.err != nil }

func (e ended) why() string {
	why := fmt.Sprintf("exit status %d", e.code)
	if e.err != nil {
		why = e.err.Error()
	}
	if len(e.stderr) > 0 {
		why += ": " + strings.Join(e.stderr, " · ")
	}
	return why
}

func (a *agent) step(ctx context.Context, dir string, env, argv []string, out, errOut func([]byte), feed func(ctx context.Context, stdin io.Writer)) ended {
	name := filepath.Base(argv[0])
	cmd := tracing.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = dir, env
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = a.config.StopGrace
	var tail []string
	stdout, stderr := lineWriter(out), lineWriter(func(line []byte) {
		errOut(line)
		if text := strings.TrimSpace(string(line)); text != "" {
			tail = append(tail, text)
			if len(tail) > stepTail {
				tail = tail[1:]
			}
		}
	})
	cmd.Stdout, cmd.Stderr = stdout, stderr
	var stdin io.WriteCloser
	if feed != nil {
		var err error
		if stdin, err = cmd.StdinPipe(); err != nil {
			return ended{code: -1, err: fmt.Errorf("start %s: %w", name, err)}
		}
	}
	if err := cmd.Start(); err != nil {
		return ended{code: -1, err: fmt.Errorf("start %s: %w", name, err)}
	}
	stopFeeding := func() {}
	if feed != nil {
		feeding, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			feed(feeding, stdin)
		}()
		stopFeeding = func() {
			stop()
			<-done
			_ = stdin.Close()
		}
	}
	exit := cmd.Wait()
	stdout.Close()
	stderr.Close()
	stopFeeding()
	end := ended{stderr: tail}
	var exited *exec.ExitError
	switch {
	case exit == nil:
	case errors.As(exit, &exited):
		end.code = exited.ExitCode()
		if status, ok := exited.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			end.code = 128 + int(status.Signal())
		}
	case errors.Is(exit, exec.ErrWaitDelay) && cmd.ProcessState.Success():
	default:
		end.code, end.err = -1, fmt.Errorf("run %s: %w", name, exit)
	}
	return end
}

var noTelemetry = []string{"OCEL_TELEMETRY=0", "DO_NOT_TRACK=1"}

func (a *agent) installEnv(home string) []string {
	return append(append(withoutToken(a.config.Env), "HOME="+home), noTelemetry...)
}

func (a *agent) runEnv(dir, home string) []string {
	env := append(withoutToken(a.config.Env),
		runner.ContextEnvVar+"="+filepath.Join(dir, jobContextFile),
		"HOME="+home,
	)
	return append(env, noTelemetry...)
}

func (a *agent) fail(ctx context.Context, code string, err error) error {
	return a.finish(ctx, &runnerv1.FinishRequest{ExitCode: -1, Error: err.Error(), ErrorCode: code})
}

func (a *agent) finish(ctx context.Context, request *runnerv1.FinishRequest) error {
	_, err := retry(ctx, func(ctx context.Context) (*runnerv1.FinishResponse, error) {
		return a.client.Finish(ctx, request)
	})
	if err != nil && !errors.Is(err, errJobOver) {
		return a.lost(err)
	}
	return nil
}

func (a *agent) lost(err error) error {
	if errors.Is(err, errJobOver) {
		return nil
	}
	return fmt.Errorf("agent: report the job: %w", err)
}

func (a *agent) blobs(urls map[string]string) func(ctx context.Context, digest string) (io.ReadCloser, error) {
	return func(ctx context.Context, digest string) (io.ReadCloser, error) {
		url, ok := urls[digest]
		if !ok {
			return nil, fmt.Errorf("the job's spec signs no URL for blob %s", digest)
		}
		return retry(ctx, func(ctx context.Context) (io.ReadCloser, error) {
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return nil, err
			}
			response, err := a.config.HTTP.Do(request)
			if err != nil {
				return nil, connect.NewError(connect.CodeUnavailable, err)
			}
			if response.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
				response.Body.Close()
				code := connect.CodeFailedPrecondition
				if response.StatusCode >= 500 || response.StatusCode == http.StatusTooManyRequests {
					code = connect.CodeUnavailable
				}
				return nil, connect.NewError(code, fmt.Errorf("fetch blob %s: %s: %s", digest, response.Status, bytes.TrimSpace(body)))
			}
			return response.Body, nil
		})
	}
}

func (a *agent) answer(ctx context.Context, out *output, stdin io.Writer) {
	taken := 0
	for {
		if !out.awaitAsk(ctx, taken) {
			return
		}
		got, err := retry(ctx, func(ctx context.Context) (*runnerv1.AwaitAnswerResponse, error) {
			return a.client.AwaitAnswer(ctx, &runnerv1.AwaitAnswerRequest{Taken: int32(taken)})
		})
		if err != nil {
			if errors.Is(err, errJobOver) {
				out.end()
			}
			return
		}
		line := got.GetLine()
		if line == "" {
			continue
		}
		if !strings.HasSuffix(line, "\n") {
			line += "\n"
		}
		if _, err := io.WriteString(stdin, line); err != nil {
			return
		}
		taken++
	}
}

type output struct {
	client runnerv1connect.JobAgentServiceClient

	mu                   sync.Mutex
	lines, stderr        []string
	offset, stderrOffset int64
	asks                 int
	changed              chan struct{}
	gone                 bool
	last                 time.Time
	stop                 func()
}

func newOutput(client runnerv1connect.JobAgentServiceClient) *output {
	return &output{client: client, changed: make(chan struct{}), last: time.Now()}
}

func (o *output) line(text string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.lines = append(o.lines, text)
}

func (o *output) errLine(text string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.stderr = append(o.stderr, text)
}

func (o *output) output(stream progressv1.Stream) func(text []byte) {
	return func(text []byte) {
		line, err := protojson.Marshal(&streamv1.RunEvent{Operation: &progressv1.OperationEvent{
			Time: timestamppb.Now(), Level: progressv1.Level_LEVEL_INFO, Message: string(text),
			Body: &progressv1.OperationEvent_Output{Output: &progressv1.Output{Stream: stream}},
		}})
		if err == nil {
			o.line(string(line))
		}
	}
}

func (o *output) say(message string) {
	line, err := protojson.Marshal(&streamv1.RunEvent{Operation: &progressv1.OperationEvent{
		Time: timestamppb.Now(), Level: progressv1.Level_LEVEL_INFO, Message: message,
		Body: &progressv1.OperationEvent_Started{Started: &progressv1.Started{}},
	}})
	if err == nil {
		o.line(string(line))
	}
}

func (o *output) asked() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.asks++
	close(o.changed)
	o.changed = make(chan struct{})
}

func (o *output) awaitAsk(ctx context.Context, taken int) bool {
	for {
		o.mu.Lock()
		asks, changed, gone := o.asks, o.changed, o.gone
		o.mu.Unlock()
		if gone {
			return false
		}
		if asks > taken {
			return true
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return false
		}
	}
}

func (o *output) end() {
	o.mu.Lock()
	o.gone = true
	stop := o.stop
	close(o.changed)
	o.changed = make(chan struct{})
	o.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (o *output) over() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.gone
}

func (o *output) loop(ctx context.Context, interval, beat time.Duration, stop func()) {
	o.mu.Lock()
	o.stop = stop
	o.mu.Unlock()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		o.mu.Lock()
		due := len(o.lines) > 0 || len(o.stderr) > 0 || time.Since(o.last) >= beat
		o.mu.Unlock()
		if !due {
			continue
		}
		cancel, err := o.flush(ctx)
		switch {
		case errors.Is(err, errJobOver):
			o.end()
			return
		case err != nil:
			o.mu.Lock()
			silent := time.Since(o.last)
			o.mu.Unlock()
			if silent > giveUpAfter {
				o.end()
				return
			}
		case cancel:
			stop()
		}
	}
}

func (o *output) drain(ctx context.Context) error {
	for {
		o.mu.Lock()
		empty := len(o.lines) == 0 && len(o.stderr) == 0
		o.mu.Unlock()
		if empty {
			return nil
		}
		if _, err := o.flush(ctx); err != nil {
			return err
		}
	}
}

func (o *output) flush(ctx context.Context) (bool, error) {
	o.mu.Lock()
	request := &runnerv1.AppendOutputRequest{Offset: o.offset, StderrOffset: o.stderrOffset}
	budget := maxOutputBytes
	for _, line := range o.lines {
		if len(request.Lines) > 0 && len(line) > budget {
			break
		}
		request.Lines = append(request.Lines, line)
		budget -= len(line)
	}
	for _, line := range o.stderr {
		if len(request.Lines)+len(request.Stderr) > 0 && len(line) > budget {
			break
		}
		request.Stderr = append(request.Stderr, line)
		budget -= len(line)
	}
	o.mu.Unlock()

	appended, err := retry(ctx, func(ctx context.Context) (*runnerv1.AppendOutputResponse, error) {
		return o.client.AppendOutput(ctx, request)
	})
	if err != nil {
		return false, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.lines = slices.Delete(o.lines, 0, len(request.Lines))
	o.stderr = slices.Delete(o.stderr, 0, len(request.Stderr))
	o.offset += int64(len(request.Lines))
	o.stderrOffset += int64(len(request.Stderr))
	o.last = time.Now()
	return appended.GetCancel(), nil
}

func retry[T any](ctx context.Context, call func(context.Context) (T, error)) (T, error) {
	deadline := time.Now().Add(time.Minute)
	for wait := 250 * time.Millisecond; ; wait = min(2*wait, 10*time.Second) {
		got, err := call(ctx)
		if err == nil {
			return got, nil
		}
		switch connect.CodeOf(err) {
		case connect.CodeUnauthenticated, connect.CodePermissionDenied, connect.CodeNotFound:
			return got, fmt.Errorf("%w: %w", errJobOver, err)
		case connect.CodeUnavailable, connect.CodeDeadlineExceeded, connect.CodeResourceExhausted, connect.CodeAborted, connect.CodeUnknown, connect.CodeInternal:
		default:
			return got, err
		}
		if ctx.Err() != nil || time.Now().Add(wait).After(deadline) {
			return got, err
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return got, err
		}
	}
}

func bearer(token string) connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
			request.Header().Set("Authorization", "Bearer "+token)
			return next(ctx, request)
		}
	})
}

func withoutToken(env []string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(kv string) bool { return strings.HasPrefix(kv, runner.TokenEnvVar+"=") })
}

func orDefault(d, fallback time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return fallback
}

func writeJobContext(dir string, spec *runnerv1.GetSpecResponse, tokenFile string) error {
	job := &runnerv1.JobContext{
		JobId: spec.GetJobId(), ProjectId: spec.GetProjectId(), Tier: spec.GetTier(), DeploymentId: spec.GetDeploymentId(),
		Slug: spec.GetSlug(), Git: spec.GetJob().GetGit(), PreviewLabel: spec.GetJob().GetPreview().GetLabel(),
		ConsoleUrl: spec.GetConsoleUrl(), JobTokenFile: tokenFile,
	}
	if err := protovalidate.Validate(job); err != nil {
		return fmt.Errorf("the job's spec makes no valid job context: %w", err)
	}
	encoded, err := protojson.Marshal(job)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, jobContextFile), encoded, 0o644)
}

func buildIDs(source, projectPath string) []*runnerv1.AppBuild {
	root, err := os.OpenRoot(source)
	if err != nil {
		return nil
	}
	defer root.Close()
	appsDir := path.Join(projectPath, filepath.ToSlash(buildoutput.AppsRoot(buildoutput.Dir)))
	entries, err := fs.ReadDir(root.FS(), appsDir)
	if err != nil {
		return nil
	}
	var builds []*runnerv1.AppBuild
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		raw, err := root.ReadFile(path.Join(appsDir, entry.Name(), buildIDFile))
		if err != nil || len(raw) > 128 {
			continue
		}
		id := strings.TrimSpace(string(raw))
		if naming.ValidateBuildID(id) == nil {
			builds = append(builds, &runnerv1.AppBuild{App: entry.Name(), BuildId: id})
		}
	}
	slices.SortFunc(builds, func(a, b *runnerv1.AppBuild) int { return strings.Compare(a.App, b.App) })
	return builds
}

type lines struct {
	each    func(line []byte)
	pending []byte
}

func lineWriter(each func(line []byte)) *lines { return &lines{each: each} }

func (l *lines) Write(p []byte) (int, error) {
	l.pending = append(l.pending, p...)
	start := 0
	for {
		end := bytes.IndexByte(l.pending[start:], '\n')
		if end < 0 {
			break
		}
		l.each(l.pending[start : start+end])
		start += end + 1
	}
	l.pending = append(l.pending[:0], l.pending[start:]...)
	if len(l.pending) > maxLine {
		l.each(l.pending)
		l.pending = l.pending[:0]
	}
	return len(p), nil
}

func (l *lines) Close() {
	if len(l.pending) > 0 {
		l.each(l.pending)
		l.pending = nil
	}
}

func relative(source, dir string) string {
	rel, err := filepath.Rel(source, dir)
	if err != nil || rel == "." {
		return "the project's root"
	}
	return filepath.ToSlash(rel)
}

func lookPath(name string, env []string) (string, error) {
	for i := len(env) - 1; i >= 0; i-- {
		dirs, found := strings.CutPrefix(env[i], "PATH=")
		if !found {
			continue
		}
		for _, dir := range filepath.SplitList(dirs) {
			candidate := filepath.Join(dir, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
				return candidate, nil
			}
		}
		break
	}
	return "", fmt.Errorf("%s is not on the job's PATH", name)
}

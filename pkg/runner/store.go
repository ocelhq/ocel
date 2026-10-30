package runner

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/pkg/endpoint"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
)

const (
	maxJobEvents   = 50000
	maxJobLogBytes = 4 << 20
	maxStderrLines = 2000
	stderrTail     = 5
)

type record struct {
	Organization      string          `json:"organization"`
	Console           string          `json:"console"`
	Token             endpoint.Job    `json:"token"`
	Request           json.RawMessage `json:"request"`
	Job               json.RawMessage `json:"job"`
	Events            uint64          `json:"events"`
	Logged            int             `json:"logged,omitempty"`
	Dropped           bool            `json:"dropped,omitempty"`
	Handle            string          `json:"handle,omitempty"`
	StopPending       bool            `json:"stopPending,omitempty"`
	Beat              time.Time       `json:"beat,omitzero"`
	CancelRequested   bool            `json:"cancelRequested,omitempty"`
	CancelRequestedAt time.Time       `json:"cancelRequestedAt,omitzero"`
	Lines             int64           `json:"lines,omitempty"`
	StderrLines       int64           `json:"stderrLines,omitempty"`
	StderrTail        []string        `json:"stderrTail,omitempty"`
	Summary           json.RawMessage `json:"summary,omitempty"`
	Answers           []string        `json:"answers,omitempty"`
	Answering         string          `json:"answering,omitempty"`
}

type heldJob struct {
	record  record
	job     *runnerv1.Job
	request *runnerv1.JobRequest
	events  []*runnerv1.JobEvent
	changed chan struct{}
}

type jobKey struct{ organization, id string }

func (s *Server) jobDir(key jobKey) string {
	return filepath.Join(s.dir, "jobs", hex.EncodeToString([]byte(key.organization)), key.id)
}

func (s *Server) loadJobs() error {
	root := filepath.Join(s.dir, "jobs")
	organizations, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, organization := range organizations {
		jobs, err := os.ReadDir(filepath.Join(root, organization.Name()))
		if err != nil {
			return err
		}
		for _, entry := range jobs {
			dir := filepath.Join(root, organization.Name(), entry.Name())
			h, err := readHeldJob(dir)
			if errors.Is(err, fs.ErrNotExist) {
				if err := os.RemoveAll(dir); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return fmt.Errorf("read the job in %s: %w", dir, err)
			}
			s.jobs[jobKey{h.record.Organization, h.job.GetId()}] = h
		}
	}
	return nil
}

func readHeldJob(dir string) (*heldJob, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "job.json"))
	if err != nil {
		return nil, err
	}
	h := &heldJob{job: &runnerv1.Job{}, request: &runnerv1.JobRequest{}, changed: make(chan struct{})}
	if err := json.Unmarshal(raw, &h.record); err != nil {
		return nil, err
	}
	if err := protojson.Unmarshal(h.record.Job, h.job); err != nil {
		return nil, err
	}
	if err := protojson.Unmarshal(h.record.Request, h.request); err != nil {
		return nil, err
	}
	if err := truncateEvents(filepath.Join(dir, "events.jsonl"), h.record.Events); err != nil {
		return nil, err
	}
	return h, nil
}

func truncateEvents(path string, kept uint64) error {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	at, lines := 0, uint64(0)
	for lines < kept {
		next := bytes.IndexByte(raw[at:], '\n')
		if next < 0 {
			break
		}
		at += next + 1
		lines++
	}
	if at == len(raw) {
		return nil
	}
	return os.Truncate(path, int64(at))
}

func (s *Server) save(key jobKey, h *heldJob) error {
	dir := s.jobDir(key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if len(h.events) > 0 {
		var lines bytes.Buffer
		for _, event := range h.events {
			raw, err := protojson.Marshal(event)
			if err != nil {
				return err
			}
			lines.Write(raw)
			lines.WriteByte('\n')
		}
		file, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		_, err = file.Write(lines.Bytes())
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
	}
	next := h.record
	next.Events += uint64(len(h.events))
	var err error
	if next.Job, err = protojson.Marshal(h.job); err != nil {
		return err
	}
	if next.Request, err = protojson.Marshal(h.request); err != nil {
		return err
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := writeFileAtomically(filepath.Join(dir, "job.json"), raw, 0o600); err != nil {
		return errors.Join(err, truncateEvents(filepath.Join(dir, "events.jsonl"), h.record.Events))
	}
	h.record, h.events = next, nil
	close(h.changed)
	h.changed = make(chan struct{})
	return nil
}

func (s *Server) readEvents(key jobKey, after uint64, limit int, kept uint64) ([]*runnerv1.JobEvent, error) {
	file, err := os.Open(filepath.Join(s.jobDir(key), "events.jsonl"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var events []*runnerv1.JobEvent
	scanner := bufio.NewScanner(file)
	scanner.Buffer(nil, 1<<20)
	for seq := uint64(1); seq <= kept && len(events) < limit && scanner.Scan(); seq++ {
		if seq <= after {
			continue
		}
		event := &runnerv1.JobEvent{}
		if err := protojson.Unmarshal(scanner.Bytes(), event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, scanner.Err()
}

func (h *heldJob) event(now time.Time, progress Progress) {
	if progress.Message == "" || h.record.Dropped {
		return
	}
	seq := h.record.Events + uint64(len(h.events)) + 1
	if seq > maxJobEvents || h.record.Logged+len(progress.Message) > maxJobLogBytes {
		h.record.Dropped = true
		progress = Progress{Level: runnerv1.JobEventLevel_JOB_EVENT_LEVEL_WARN, Message: fmt.Sprintf("The job's log holds at most %d lines and %d MiB; the rest are dropped", maxJobEvents, maxJobLogBytes>>20)}
	}
	h.events = append(h.events, newEvent(seq, now, h.job, progress))
	h.record.Logged += len(progress.Message)
}

func (h *heldJob) apply(now time.Time, progress Progress) {
	if progress.Phase != runnerv1.JobPhase_JOB_PHASE_UNSPECIFIED {
		h.job.Phase = progress.Phase
	}
	if progress.Step != "" {
		h.job.Step = progress.Step
	}
	switch {
	case progress.Input != nil:
		h.job.State = runnerv1.JobState_JOB_STATE_INPUT_REQUIRED
		h.job.Input = progress.Input
	case progress.Resumed:
		h.job.State = runnerv1.JobState_JOB_STATE_RUNNING
		h.job.Input = nil
	}
	h.event(now, progress)
}

func (h *heldJob) start(now time.Time) {
	h.record.Beat = now
	if h.job.GetState() == runnerv1.JobState_JOB_STATE_QUEUED {
		h.job.State = runnerv1.JobState_JOB_STATE_RUNNING
		h.job.StartedAt = timestamppb.New(now)
		h.job.Step = "Starting"
	}
}

func (h *heldJob) forgetGitToken() {
	if git := h.request.GetGitSource(); git != nil {
		git.Token = ""
	}
}

func (h *heldJob) end(now time.Time, state runnerv1.JobState, jobErr *runnerv1.JobError) {
	h.forgetGitToken()
	h.job.State, h.job.Error, h.job.Input = state, jobErr, nil
	h.job.EndedAt = timestamppb.New(now)
	h.record.Answering = ""
	last := Progress{Level: runnerv1.JobEventLevel_JOB_EVENT_LEVEL_ERROR, Message: "The job failed: " + jobErr.GetMessage()}
	switch state {
	case runnerv1.JobState_JOB_STATE_SUCCEEDED:
		last = Progress{Level: runnerv1.JobEventLevel_JOB_EVENT_LEVEL_INFO, Message: "The job succeeded"}
	case runnerv1.JobState_JOB_STATE_CANCELED:
		h.job.Step = "Canceled"
		last = Progress{Level: runnerv1.JobEventLevel_JOB_EVENT_LEVEL_WARN, Message: "The job was canceled"}
	}
	h.events = append(h.events, newEvent(h.record.Events+uint64(len(h.events))+1, now, h.job, last))
}

func (h *heldJob) summary() (*streamv1.RunSummary, error) {
	if len(h.record.Summary) == 0 {
		return nil, nil
	}
	summary := &streamv1.RunSummary{}
	return summary, protojson.Unmarshal(h.record.Summary, summary)
}

func newEvent(seq uint64, now time.Time, job *runnerv1.Job, progress Progress) *runnerv1.JobEvent {
	phase := progress.Phase
	if phase == runnerv1.JobPhase_JOB_PHASE_UNSPECIFIED {
		phase = job.GetPhase()
	}
	return &runnerv1.JobEvent{Seq: seq, Time: timestamppb.New(now), Level: progress.Level, Phase: phase, Message: progress.Message}
}

func (h *heldJob) holdsSlot() bool {
	return !ended(h.job.GetState()) || h.record.StopPending
}

func ended(state runnerv1.JobState) bool {
	switch state {
	case runnerv1.JobState_JOB_STATE_SUCCEEDED, runnerv1.JobState_JOB_STATE_FAILED, runnerv1.JobState_JOB_STATE_CANCELED:
		return true
	}
	return false
}

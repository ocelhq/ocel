package runner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/protobuf/proto"

	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
)

func (s *Server) SweepEvery(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case <-ticker.C:
			_ = s.Sweep(ctx)
		}
	}
}

func (s *Server) Sweep(ctx context.Context) error {
	now := s.now()
	type stopping struct {
		key    jobKey
		handle string
	}
	var stop []stopping
	var errs []error
	s.mu.Lock()
	for key, h := range s.jobs {
		switch {
		case h.record.StopPending:
			stop = append(stop, stopping{key, h.record.Handle})
			continue
		case ended(h.job.GetState()):
			if now.After(latest(h.record.Token.Expires, h.job.GetEndedAt().AsTime()).Add(retention)) {
				if err := os.RemoveAll(s.jobDir(key)); err != nil {
					errs = append(errs, err)
					continue
				}
				delete(s.jobs, key)
			}
			continue
		case !overdue(h, now, s.cancelGrace):
			continue
		}
		h.record.StopPending = true
		if err := s.save(key, h); err != nil {
			errs = append(errs, fmt.Errorf("end job %s: %w", key.id, err))
			continue
		}
		stop = append(stop, stopping{key, h.record.Handle})
	}
	referenced := map[string]bool{}
	for key, h := range s.jobs {
		if id := h.request.GetSourceId(); id != "" {
			referenced[s.sourcePath(key.organization, id)] = true
		}
	}
	s.mu.Unlock()

	for _, each := range stop {
		if err := s.launcher.Cancel(ctx, each.handle); err != nil {
			errs = append(errs, fmt.Errorf("stop the agent %s: %w", each.handle, err))
			continue
		}
		if _, err := s.change(each.key, func(h *heldJob) (bool, error) {
			stopped := h.record.StopPending
			h.record.StopPending = false
			return stopped, nil
		}); err != nil {
			errs = append(errs, fmt.Errorf("record the agent %s stopped: %w", each.handle, err))
		}
	}
	if err := s.forgetSources(now, referenced); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func overdue(h *heldJob, now time.Time, grace time.Duration) bool {
	created := h.job.GetCreatedAt().AsTime()
	queued := h.job.GetState() == runnerv1.JobState_JOB_STATE_QUEUED
	silent := !queued && now.Sub(h.record.Beat) > beatGrace
	switch {
	case !now.Before(h.record.Token.Expires):
		h.end(now, runnerv1.JobState_JOB_STATE_FAILED, &runnerv1.JobError{Code: CodeTimeout, Message: "the job ran past its token's expiry and was stopped"})
	case queued && now.Sub(created) > launchGrace:
		h.end(now, runnerv1.JobState_JOB_STATE_FAILED, &runnerv1.JobError{Code: CodeInterrupted, Message: "the job's agent never started: start the job again"})
	case h.record.CancelRequested && now.Sub(h.record.CancelRequestedAt) > grace:
		h.end(now, runnerv1.JobState_JOB_STATE_CANCELED, nil)
	case silent && h.record.CancelRequested:
		h.end(now, runnerv1.JobState_JOB_STATE_CANCELED, nil)
	case silent:
		h.end(now, runnerv1.JobState_JOB_STATE_FAILED, &runnerv1.JobError{Code: CodeInterrupted, Message: "the job's agent stopped reporting: start the job again"})
	default:
		return false
	}
	return true
}

func latest(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (s *Server) forgetSources(now time.Time, referenced map[string]bool) error {
	used := map[string]bool{}
	var errs []error
	root := filepath.Join(s.dir, "sources")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !referenced[path] && now.Sub(info.ModTime()) > retention {
			errs = append(errs, os.Remove(path))
			return nil
		}
		encoded, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		manifest := &runnerv1.SourceManifest{}
		if err := proto.Unmarshal(encoded, manifest); err != nil {
			return err
		}
		organization := filepath.Base(filepath.Dir(path))
		for _, file := range manifest.GetFiles() {
			used[filepath.Join(organization, file.GetDigest())] = true
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	err = filepath.WalkDir(s.blobs.dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !digestPattern.MatchString(entry.Name()) {
			return err
		}
		rel, err := filepath.Rel(s.blobs.dir, path)
		if err != nil || used[rel] {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if now.Sub(info.ModTime()) > retention {
			errs = append(errs, os.Remove(path))
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

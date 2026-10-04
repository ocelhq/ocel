package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
)

type LogTarget struct {
	App, Release, Source string
	Function             *Function
	Container            *AppContainer
}

func (t LogTarget) Physical() string {
	switch {
	case t.Function != nil:
		return t.Function.Physical
	case t.Container != nil:
		return t.Container.Physical
	}
	return ""
}

func (t LogTarget) Revision() string {
	switch {
	case t.Function != nil:
		return t.Function.Revision
	case t.Container != nil:
		return t.Container.Revision
	}
	return ""
}

type LogQuery struct {
	Tier         environment.Tier
	Slug, Env    string
	Targets      []LogTarget
	Since, Until time.Time
	Limit        int
	Contains     string
	Tail         bool
}

type LogStream string

const (
	LogStreamStdout LogStream = "stdout"
	LogStreamStderr LogStream = "stderr"
)

type LogEntry struct {
	Time     time.Time
	App      string
	Source   string
	Release  string
	Instance string
	Stream   LogStream
	Message  string
	Severity string
	Failure  bool
}

type LogNoticeKind int

const (
	LogSampled LogNoticeKind = iota + 1
	LogReconnected
	LogSourceGone
)

type LogNotice struct {
	Kind    LogNoticeKind
	Omitted int
	Target  LogTarget
}

type Logs interface {
	Read(ctx context.Context, q LogQuery, emit func([]LogEntry) error, notice func(LogNotice) error) error
}

type LogTargetsMissing struct{ Targets []LogTarget }

func (e LogTargetsMissing) Error() string {
	return fmt.Sprintf("the logs of %d targets no longer exist", len(e.Targets))
}

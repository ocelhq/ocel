package main

import (
	"time"

	"github.com/ocelhq/ocel/cli/internal/cli/runui"
)

type script struct {
	at time.Duration
	ev []runui.Envelope
}

func (s *script) push(e runui.Envelope) {
	e.At = s.at
	s.ev = append(s.ev, e)
}

func (s *script) wait(d time.Duration) { s.at += d }

func (s *script) plan(p *runui.Plan) { s.push(runui.Envelope{Plan: p}) }

func (s *script) declare(decls ...runui.StageDecl) {
	s.push(runui.Envelope{Stages: decls})
}

func (s *script) prog(id, msg string) {
	s.push(runui.Envelope{Progress: &runui.Progress{StageID: id, Message: msg}})
}

func (s *script) cached(id, msg string) {
	s.push(runui.Envelope{Progress: &runui.Progress{StageID: id, Message: msg, Cached: true}})
}

func (s *script) bar(id, msg string, cur, total uint32) {
	s.push(runui.Envelope{Progress: &runui.Progress{
		StageID: id, Message: msg, Current: cur, Total: total, HasBar: true,
	}})
}

func (s *script) log(id string, lines ...string) {
	for _, line := range lines {
		s.push(runui.Envelope{Log: &runui.Log{StageID: id, Line: line}})
		s.at += 90 * time.Millisecond
	}
}

func (s *script) end(id string) {
	s.push(runui.Envelope{End: &runui.StageEnd{StageID: id}})
}

func (s *script) failed(id string) {
	s.push(runui.Envelope{End: &runui.StageEnd{StageID: id, Failed: true}})
}

func (s *script) result(r *runui.Result) { s.push(runui.Envelope{Result: r}) }

func stage(id, title string) runui.StageDecl { return runui.StageDecl{ID: id, Title: title} }

func child(id, parent, title string) runui.StageDecl {
	return runui.StageDecl{ID: id, Parent: parent, Title: title}
}

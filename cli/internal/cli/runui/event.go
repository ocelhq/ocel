package runui

import "time"

type Action int

const (
	Keep Action = iota
	Create
	Update
	Replace
	Delete
	DisableThenDelete
)

type Row struct {
	Kind   string
	Name   string
	Action Action
	Reason string
	Slow   bool
}

type Group struct {
	Kind    string
	Name    string
	Feature string
	Rows    []Row
}

type Plan struct {
	Subject  string
	EdgeKind string
	Groups   []Group
}

type StageDecl struct {
	ID     string
	Parent string
	Title  string
}

type Progress struct {
	StageID string
	Message string
	Current uint32
	Total   uint32
	HasBar  bool
	Cached  bool
}

type Log struct {
	StageID string
	Line    string
}

type StageEnd struct {
	StageID string
	Failed  bool
}

type Result struct {
	Success    bool
	Headline   string
	Error      string
	AppURLs    []string
	Withheld   string
	StreamAt   string
	Diagnostic []string
}

type Envelope struct {
	At time.Duration

	Plan     *Plan
	Stages   []StageDecl
	Progress *Progress
	Log      *Log
	End      *StageEnd
	Result   *Result
}

type Script struct {
	Command string
	Events  []Envelope
}

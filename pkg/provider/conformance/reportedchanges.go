package conformance

import (
	"cmp"
	"slices"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

type writtenRow struct {
	Kind   string
	Name   string
	Action provider.ChangeAction
}

func compareWrittenRows(a, b writtenRow) int {
	return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Name, b.Name), cmp.Compare(a.Action, b.Action))
}

func listPlannedWrites(plan provider.Plan) []writtenRow {
	var rows []writtenRow
	for _, group := range plan.Groups {
		for _, change := range group.Changes {
			if change.Action.Writes() {
				rows = append(rows, writtenRow{Kind: change.Kind, Name: change.Name, Action: change.Action})
			}
		}
	}
	slices.SortFunc(rows, compareWrittenRows)
	return slices.Compact(rows)
}

type reportedChanges struct {
	mu   sync.Mutex
	rows []writtenRow
}

func (r *reportedChanges) Say(string)    {}
func (r *reportedChanges) Warn(string)   {}
func (r *reportedChanges) Error(string)  {}
func (r *reportedChanges) Detail(string) {}
func (r *reportedChanges) Debug(string)  {}

func (r *reportedChanges) Span(_ string, _, _ time.Time, err error, attrs ...progress.Attr) {
	var row writtenRow
	for _, attr := range attrs {
		switch attr.Key {
		case progress.AttrKeyResourceType:
			row.Kind = attr.Value
		case progress.AttrKeyResourceName:
			row.Name = attr.Value
		case progress.AttrKeyResourceAction:
			row.Action = provider.ChangeAction(attr.Value)
		}
	}
	if err != nil || !row.Action.Writes() || row.Action == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, row)
}

func (r *reportedChanges) listWrites() []writtenRow {
	r.mu.Lock()
	defer r.mu.Unlock()
	rows := slices.Clone(r.rows)
	slices.SortFunc(rows, compareWrittenRows)
	return slices.Compact(rows)
}

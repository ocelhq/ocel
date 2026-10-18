package component

import (
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

var actionWeights = map[provider.ChangeAction]int{
	provider.ActionUpdate:  1,
	provider.ActionReplace: 2,
	provider.ActionCreate:  3,
}

type Change struct {
	action  provider.ChangeAction
	reasons []string
}

func (c *Change) Add(action provider.ChangeAction, reason string) {
	if !action.Writes() {
		return
	}
	if actionWeights[action] > actionWeights[c.action] {
		c.action = action
	}
	if reason != "" {
		c.reasons = append(c.reasons, reason)
	}
}

func (c *Change) AddRecorded(recorded *provider.Binding, binding provider.Binding, unknown ...string) {
	if recorded == nil {
		return
	}
	for _, name := range stackrecords.ListRecordedProperties(binding.Type) {
		want, known := binding.Properties[name]
		if !known || slices.Contains(unknown, name) {
			continue
		}
		if held := recorded.Properties[name]; held != want {
			c.Add(provider.ActionUpdate, "its recorded "+name+" changes from "+strconv.Quote(held)+" to "+strconv.Quote(want))
		}
	}
}

func (c Change) Report() (provider.ChangeAction, string, error) {
	if c.action == "" {
		return provider.ActionKeep, provider.ReasonCurrent, nil
	}
	return c.action, strings.Join(c.reasons, "; "), nil
}

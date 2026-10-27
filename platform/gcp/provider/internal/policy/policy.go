package policy

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
)

const (
	ConditionalVersion = 3
	MaxMembers         = 1500
)

type Condition struct {
	Title       string
	Description string
	Expression  string
}

type Binding struct {
	Role      string
	Members   []string
	Condition *Condition
}

type Write func(ctx context.Context, bindings []Binding) error

type Resource struct {
	Name string
	Read func(ctx context.Context) ([]Binding, Write, error)
}

var ErrAbsent = errors.New("the resource is not there")

type Change func(bindings []Binding) ([]Binding, bool)

func Edit(ctx context.Context, resource Resource, change Change) (bool, error) {
	var (
		edited bool
		failed error
	)
	refused := googleretry.UntilWritten(ctx, func() error {
		edited, failed = false, nil
		bindings, write, err := resource.Read(ctx)
		if err != nil {
			failed = fmt.Errorf("read who may use %s: %w", resource.Name, err)
			return nil
		}
		changed, differs := change(bindings)
		if !differs {
			return nil
		}
		if failed = refuseCrowded(resource, bindings, changed); failed != nil {
			return nil
		}
		edited = true
		return write(ctx, changed)
	})
	switch {
	case failed != nil:
		return false, failed
	case refused != nil:
		return false, fmt.Errorf("change who may use %s: %w", resource.Name, refused)
	}
	return edited, nil
}

func refuseCrowded(resource Resource, before, after []Binding) error {
	held, wanted := Members(before), Members(after)
	if wanted <= MaxMembers || wanted <= held {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"the IAM policy of %s would name %d principals, and Google allows 1,500 in one policy, counting a principal once for every binding it appears in.\n"+
			"Destroy the environments and apps it no longer runs, or deploy some of its projects into another Google Cloud project, then try again",
		resource.Name, wanted)
}

func Read(ctx context.Context, resource Resource) ([]Binding, error) {
	bindings, _, err := resource.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("read who may use %s: %w", resource.Name, err)
	}
	return bindings, nil
}

func IsAbsent(err error) bool { return errors.Is(err, ErrAbsent) }

func Absent(err error) error { return fmt.Errorf("%w: %w", ErrAbsent, err) }

func sameCondition(current, want *Condition) bool {
	if current == nil || want == nil {
		return current == nil && want == nil
	}
	return current.Expression == want.Expression
}

func Holds(bindings []Binding, role, member string, condition *Condition) bool {
	return slices.ContainsFunc(bindings, func(binding Binding) bool {
		return binding.Role == role && sameCondition(binding.Condition, condition) && slices.Contains(binding.Members, member)
	})
}

func Grant(role, member string, condition *Condition) Change {
	return func(bindings []Binding) ([]Binding, bool) {
		for i, binding := range bindings {
			if binding.Role != role || !sameCondition(binding.Condition, condition) {
				continue
			}
			if slices.Contains(binding.Members, member) {
				return bindings, false
			}
			granted := slices.Clone(bindings)
			granted[i].Members = append(slices.Clone(binding.Members), member)
			return granted, true
		}
		return append(slices.Clone(bindings), Binding{Role: role, Members: []string{member}, Condition: condition}), true
	}
}

func Revoke(role, member string, condition *Condition) Change {
	return revokeWhere(member, func(binding Binding) bool {
		return binding.Role == role && sameCondition(binding.Condition, condition)
	})
}

func RevokeRoles(member string, roles []string) Change {
	return revokeWhere(member, func(binding Binding) bool { return slices.Contains(roles, binding.Role) })
}

func revokeWhere(member string, matches func(Binding) bool) Change {
	return func(bindings []Binding) ([]Binding, bool) {
		kept := make([]Binding, 0, len(bindings))
		changed := false
		for _, binding := range bindings {
			if !matches(binding) || !slices.Contains(binding.Members, member) {
				kept = append(kept, binding)
				continue
			}
			changed = true
			binding.Members = slices.DeleteFunc(slices.Clone(binding.Members), func(held string) bool { return held == member })
			if len(binding.Members) > 0 {
				kept = append(kept, binding)
			}
		}
		if !changed {
			return bindings, false
		}
		return kept, true
	}
}

func OnlyMember(role, member string) Change {
	return func(bindings []Binding) ([]Binding, bool) {
		kept := make([]Binding, 0, len(bindings)+1)
		var held []Binding
		for _, binding := range bindings {
			if binding.Role == role {
				held = append(held, binding)
				continue
			}
			kept = append(kept, binding)
		}
		only := []string{member}
		if len(held) == 1 && held[0].Condition == nil && slices.Equal(held[0].Members, only) {
			return bindings, false
		}
		return append(kept, Binding{Role: role, Members: only}), true
	}
}

func Each(changes ...Change) Change {
	return func(bindings []Binding) ([]Binding, bool) {
		changed := false
		for _, change := range changes {
			var differs bool
			bindings, differs = change(bindings)
			changed = changed || differs
		}
		return bindings, changed
	}
}

func IsConditioned(bindings []Binding) bool {
	return slices.ContainsFunc(bindings, func(binding Binding) bool { return binding.Condition != nil })
}

func Members(bindings []Binding) int {
	total := 0
	for _, binding := range bindings {
		total += len(binding.Members)
	}
	return total
}

func GrantOnly(member string, wanted []Binding) Change {
	changes := []Change{revokeWhere(member, func(binding Binding) bool {
		return !slices.ContainsFunc(wanted, func(want Binding) bool {
			return want.Role == binding.Role && sameCondition(binding.Condition, want.Condition)
		})
	})}
	for _, want := range wanted {
		changes = append(changes, Grant(want.Role, member, want.Condition))
	}
	return Each(changes...)
}

func HoldsOnly(bindings []Binding, member string, wanted []Binding) bool {
	_, differs := GrantOnly(member, wanted)(bindings)
	return !differs
}

func RevokeAll(member string) Change {
	return revokeWhere(member, func(Binding) bool { return true })
}

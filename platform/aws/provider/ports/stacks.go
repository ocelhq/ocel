package ports

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	kit "github.com/ocelhq/ocel/pkg/providerkit/ports"
)

const casAttempts = 8

type TagClock interface {
	SweepTagClock(ctx context.Context, project string, stack naming.StackName) error
}

type Stacks struct {
	Records kit.RecordStore
	Tags    TagClock
}

func (s Stacks) AddProject(ctx context.Context, project string, features []string) error {
	if err := naming.Validate("project", project); err != nil {
		return fmt.Errorf("index a project: %w", err)
	}
	name := providerkit.ProjectRecord(project)
	wanted, err := json.Marshal(providerkit.Project{Features: features})
	if err != nil {
		return fmt.Errorf("index project %s: %w", project, err)
	}
	for range casAttempts {
		held, err := kit.Held(ctx, s.Records, name)
		if err != nil {
			return fmt.Errorf("index project %s: %w", project, err)
		}
		if bytes.Equal(held.Bytes, wanted) {
			return nil
		}
		held.Bytes = wanted
		if _, err := s.Records.Write(ctx, held); err != nil {
			if errors.Is(err, kit.ErrStale) {
				continue
			}
			return fmt.Errorf("index project %s: %w", project, err)
		}
		return nil
	}
	return fmt.Errorf("index project %s: it moved under %d attempts", project, casAttempts)
}

func (s Stacks) RemoveProject(ctx context.Context, project string) error {
	if err := naming.Validate("project", project); err != nil {
		return fmt.Errorf("drop a project from the index: %w", err)
	}
	if err := kit.Forget(ctx, s.Records, providerkit.ProjectRecord(project)); err != nil {
		return fmt.Errorf("drop project %s from the index: %w", project, err)
	}
	return nil
}

func (s Stacks) AddStack(ctx context.Context, project string, stack naming.StackName) error {
	if err := readable(project, stack); err != nil {
		return fmt.Errorf("index a stack: %w", err)
	}
	if err := providerkit.WriteStack(ctx, s.Records, project, stack, providerkit.Stack{}); err != nil {
		return fmt.Errorf("index stack %s/%s: %w", project, stack, err)
	}
	return nil
}

func (s Stacks) RemoveStack(ctx context.Context, project string, stack naming.StackName) error {
	if err := readable(project, stack); err != nil {
		return fmt.Errorf("drop a stack from the index: %w", err)
	}
	if s.Tags != nil {
		if err := s.Tags.SweepTagClock(ctx, project, stack); err != nil {
			return fmt.Errorf("drop stack %s/%s from the index: %w", project, stack, err)
		}
	}
	if err := providerkit.ForgetStack(ctx, s.Records, project, stack); err != nil {
		return fmt.Errorf("drop stack %s/%s from the index: %w", project, stack, err)
	}
	return nil
}

func (s Stacks) Stacks(ctx context.Context, project string) ([]naming.StackName, error) {
	if err := naming.Validate("project", project); err != nil {
		return nil, fmt.Errorf("list a project's stacks: %w", err)
	}
	entries, err := providerkit.ReadStacks(ctx, s.Records, project)
	if err != nil {
		return nil, fmt.Errorf("list %s's stacks: %w", project, err)
	}
	stacks := make([]naming.StackName, 0, len(entries))
	for _, entry := range entries {
		stacks = append(stacks, entry.Name)
	}
	return stacks, nil
}

func (s Stacks) Projects(ctx context.Context) ([]string, error) {
	held, err := s.Records.List(ctx, providerkit.ProjectsRecord())
	if err != nil {
		return nil, fmt.Errorf("list indexed projects: %w", err)
	}
	var projects []string
	for _, record := range held {
		if len(record.Name) == 2 {
			projects = append(projects, record.Name[1])
		}
	}
	return projects, nil
}

func readable(project string, stack naming.StackName) error {
	if err := naming.Validate("project", project); err != nil {
		return err
	}
	if _, err := naming.ParseStackName(stack.String()); err != nil {
		return err
	}
	return nil
}

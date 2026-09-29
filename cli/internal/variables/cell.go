package variables

import (
	"context"
	"errors"
	"slices"
	"strings"
)

type Cell struct {
	Key    string `json:"key"`
	Folder string `json:"folder"`
}

type Coordinate struct {
	Cell        Cell
	Environment string
}

type ValueMetadata struct {
	Coordinate
	Version   int64
	Reference *Reference
	EnvSource string
}

type Reference struct {
	Slug   string `json:"slug"`
	Folder string `json:"folder"`
	Key    string `json:"key"`
}

type Override struct {
	Environment string     `json:"environment"`
	Version     int64      `json:"version"`
	Orphaned    bool       `json:"orphaned,omitempty"`
	Reference   *Reference `json:"reference,omitempty"`
}

func IsOrphaned(environments []string, environment string) bool {
	return environment != "" && !slices.Contains(environments, environment)
}

var ErrStaleValue = errors.New("stale value")

type Version struct {
	Version   int64 `json:"version"`
	CreatedAt int64 `json:"createdAt"`
	Size      int64 `json:"size"`
}

type Values interface {
	List(ctx context.Context) ([]ValueMetadata, error)

	Reveal(ctx context.Context, rows []Coordinate) (map[Coordinate]string, error)
}

func describeAll(rows []Coordinate) string {
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, describe(row.Cell))
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

func describe(cell Cell) string {
	if cell.Folder == "" {
		return cell.Key + " (project root)"
	}
	return cell.Key + " (" + cell.Folder + ")"
}

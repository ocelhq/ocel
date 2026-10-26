package variables_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/variables"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

type publishedValues struct {
	listed []variables.ValueMetadata
}

func (v publishedValues) List(context.Context) ([]variables.ValueMetadata, error) {
	return v.listed, nil
}

func (v publishedValues) Reveal(_ context.Context, rows []variables.Coordinate) (map[variables.Coordinate]string, error) {
	found := map[variables.Coordinate]string{}
	for _, row := range rows {
		found[row] = "published " + row.Cell.Key
	}
	return found, nil
}

func publishedRow(key string, secret bool) variables.ValueMetadata {
	return variables.ValueMetadata{
		Coordinate:      variables.Coordinate{Cell: variables.Cell{Key: key}},
		Version:         1,
		Publisher:       "urn:pulumi:prod::infra::pulumi-nodejs:dynamic:Resource::ocel-variables-production",
		PublishedSecret: secret,
	}
}

func TestADeployChecksTheSecrecyOfOnlyThePublishedValuesItReads(t *testing.T) {
	t.Parallel()
	api := variables.App{Name: "api"}
	handSet := variables.ValueMetadata{Coordinate: variables.Coordinate{Cell: variables.Cell{Key: "API_KEY"}}, Version: 1}
	publishedToPR2 := publishedRow("API_KEY", false)
	publishedToPR2.Environment = "pr-2"
	listed := publishedValues{listed: []variables.ValueMetadata{handSet, publishedToPR2}}

	t.Run("another preview environment's published value is not checked", func(t *testing.T) {
		t.Parallel()
		g := prefetched(t, listed, variables.Scope{Apps: []variables.App{api}, Tier: environmentv1.Tier_TIER_PREVIEW, Environment: "pr-1"})
		declare(t, g, def("API_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET))

		if _, err := g.ResolveVariables(context.Background(), []variables.App{api}); err != nil {
			t.Fatalf("ResolveVariables() in pr-1 = %v, want the hand-set tier-wide API_KEY delivered", err)
		}
	})

	t.Run("the environment's own published value is refused naming where and by whom", func(t *testing.T) {
		t.Parallel()
		g := prefetched(t, listed, variables.Scope{Apps: []variables.App{api}, Tier: environmentv1.Tier_TIER_PREVIEW, Environment: "pr-2"})
		declare(t, g, def("API_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET))

		_, err := g.ResolveVariables(context.Background(), []variables.App{api})
		if err == nil || !strings.Contains(err.Error(), "pr-2") || !strings.Contains(err.Error(), publishedToPR2.Publisher) {
			t.Fatalf("ResolveVariables() in pr-2 = %v, want it refused naming pr-2 and %s", err, publishedToPR2.Publisher)
		}
	})
}

func TestADeployRefusesAPublishedValueWhoseDeclarationDoesNotMatchItsSecrecy(t *testing.T) {
	t.Parallel()
	api := variables.App{Name: "api"}

	for name, tc := range map[string]struct {
		secret  bool
		class   resourcesv1.VariableClass
		refused bool
	}{
		"a secret declared plain":       {secret: true, class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, refused: true},
		"a secret declared sensitive":   {secret: true, class: resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, refused: true},
		"a plain value declared secret": {secret: false, class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, refused: true},
		"a secret declared secret":      {secret: true, class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET},
		"a plain value declared plain":  {secret: false, class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			g := prefetched(t, publishedValues{listed: []variables.ValueMetadata{publishedRow("API_KEY", tc.secret)}}, variables.Scope{Apps: []variables.App{api}})
			declare(t, g, def("API_KEY", tc.class))

			_, err := g.ResolveVariables(context.Background(), []variables.App{api})
			if !tc.refused {
				if err != nil {
					t.Fatalf("ResolveVariables() = %v, want the published value delivered", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "API_KEY") || !strings.Contains(err.Error(), "secret") {
				t.Fatalf("ResolveVariables() = %v, want it refused naming API_KEY and its secrecy", err)
			}
		})
	}
}

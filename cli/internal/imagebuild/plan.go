package imagebuild

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/railwayapp/railpack/core"
	"github.com/railwayapp/railpack/core/app"
)

const PlanFileName = "railpack-plan.json"

func Plan(appDir string) ([]byte, error) {
	source, err := app.NewApp(appDir)
	if err != nil {
		return nil, fmt.Errorf("read %s as an app railpack can build: %w", appDir, err)
	}
	bare, err := app.FromEnvs(nil)
	if err != nil {
		return nil, err
	}
	result, err := core.GenerateBuildPlan(source, bare, &core.GenerateBuildPlanOptions{})
	if err != nil {
		return nil, fmt.Errorf("railpack could not plan %s: %w", appDir, err)
	}
	if !result.Success {
		return nil, fmt.Errorf("railpack could not plan %s:\n%s", appDir, refusal(result))
	}
	plan, err := json.Marshal(result.Plan)
	if err != nil {
		return nil, fmt.Errorf("serialize the railpack plan for %s: %w", appDir, err)
	}
	return plan, nil
}

func refusal(result *core.BuildResult) string {
	var said []string
	for _, msg := range result.Logs {
		if line := strings.TrimSpace(msg.Msg); line != "" {
			said = append(said, "  "+line)
		}
	}
	if len(said) == 0 {
		return "  railpack recognised nothing to build in it"
	}
	return strings.Join(said, "\n")
}

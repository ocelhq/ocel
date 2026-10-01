package manifest

import (
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func TestAnInvalidCronIsRefusedAtTheTask(t *testing.T) {
	t.Parallel()

	for _, cron := range []string{"* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8", "*/0 * * * *", "5-1 * * * *", "a * * * *", "* * * * * *", "0 0 31 2 *"} {
		t.Run(cron, func(t *testing.T) {
			t.Parallel()

			_, err := assembleWorkers([]app{{Name: "web"}}, []declaredResource{task("nightly", "src/nightly.ts:2", &resourcesv1.TaskConfig{Cron: cron})}, nil)
			refusedAt(t, err, "src/nightly.ts:2", "cron")
		})
	}
}

func TestAValidCronIsAccepted(t *testing.T) {
	t.Parallel()

	for _, cron := range []string{"0 3 * * *", "*/15 * * * *", "0 9-17 * * MON-FRI", "30 2 1,15 * *", "0 0 1 JAN *", "0 12 * * 0", "0 12 * * 7"} {
		if _, err := assembleWorkers([]app{{Name: "web"}}, []declaredResource{task("nightly", "src/nightly.ts:2", &resourcesv1.TaskConfig{Cron: cron})}, nil); err != nil {
			t.Errorf("cron %q: assemble() = %v, want it accepted", cron, err)
		}
	}
}

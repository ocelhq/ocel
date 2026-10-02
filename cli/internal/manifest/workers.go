package manifest

import (
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

const defaultWorker = "worker"

type ServedTwiceError struct {
	Subject      string
	FirstSource  string
	FirstWorker  string
	SecondSource string
	SecondWorker string
}

func (e *ServedTwiceError) Error() string {
	return fmt.Sprintf(
		"%s is declared at %s served by worker %q and at %s served by worker %q: it runs on one worker, so declare it once",
		e.Subject, sourceOrUnknown(e.FirstSource), e.FirstWorker, sourceOrUnknown(e.SecondSource), e.SecondWorker,
	)
}

func joinWorkers(apps []app, ceilings []provider.WorkerCeiling, served []declaredResource, declaredWorkers map[string]declaredResource) ([]*contractv1.ManifestWorker, error) {
	firstServed := map[string]declaredResource{}
	for _, d := range served {
		name := d.consumer().GetWorker()
		if err := refuseInvalidName(d, name); err != nil {
			return nil, err
		}
		if _, declared := declaredWorkers[name]; !declared && name != defaultWorker {
			return nil, refuse(d, "names worker %q, and no worker(%q) is declared, so no worker serves it", name, name)
		}
		if _, seen := firstServed[name]; !seen {
			firstServed[name] = d
		}
	}

	var workers []*contractv1.ManifestWorker
	computes := map[string]provider.Compute{}
	names := slices.Sorted(func(yield func(string) bool) {
		for name := range declaredWorkers {
			if !yield(name) {
				return
			}
		}
		if _, served := firstServed[defaultWorker]; served {
			if _, declared := declaredWorkers[defaultWorker]; !declared {
				yield(defaultWorker)
			}
		}
	})
	for _, name := range names {
		declaration, declared := declaredWorkers[name]
		if !declared {
			declaration = firstServed[name]
		}
		joined, err := workerApp(apps, name, declaration, declared)
		if err != nil {
			return nil, err
		}
		computes[name] = joined.Compute
		workers = append(workers, &contractv1.ManifestWorker{
			Name:        name,
			Concurrency: declaration.Worker.GetConcurrency(),
			App:         joined.Name,
			Path:        joined.Path,
			Compute:     string(joined.Compute),
		})
	}

	for _, d := range served {
		if err := refuseAboveCeiling(d, computes[d.consumer().GetWorker()], ceilings); err != nil {
			return nil, err
		}
	}
	return workers, nil
}

func workerApp(apps []app, name string, declaration declaredResource, declared bool) (app, error) {
	runsOn := ""
	if !declared {
		runsOn = fmt.Sprintf("runs on worker %q, which ", name)
	}
	if i := slices.IndexFunc(apps, func(a app) bool { return a.Name == name }); i >= 0 {
		joined := apps[i]
		switch {
		case len(joined.Domains) > 0:
			return app{}, refuse(declaration, "%sjoins the app %q, and that app has domains: a worker takes no public traffic, so rename the worker or remove the app's domains", runsOn, name)
		case joined.HealthCheckPath != "":
			return app{}, refuse(declaration, "%sjoins the app %q, and that app has a health path: a worker takes no public traffic, so rename the worker or remove the app's health path", runsOn, name)
		}
		return joined, nil
	}
	if len(apps) == 1 {
		return apps[0], nil
	}
	if declared {
		if declaring, found := appDeclaring(apps, declaration.Source); found {
			return declaring, nil
		}
	}
	return app{}, refuse(declaration, "%sneeds an app, and with %d apps in ocel.json and none named %q, ocel cannot tell which app's folder and compute worker %q takes: add an app named %q to ocel.json", runsOn, len(apps), name, name, name)
}

func appDeclaring(apps []app, source string) (app, bool) {
	colon := strings.LastIndex(source, ":")
	if colon <= 0 {
		return app{}, false
	}
	file := source[:colon]
	best, depth := -1, -1
	for i, a := range apps {
		dir := path.Clean(filepath.ToSlash(a.Path))
		reach := 0
		if dir != "." {
			if !strings.HasPrefix(file, dir+"/") {
				continue
			}
			reach = len(dir)
		}
		if reach > depth {
			best, depth = i, reach
		}
	}
	if best < 0 {
		return app{}, false
	}
	return apps[best], true
}

func refuseAboveCeiling(d declaredResource, compute provider.Compute, ceilings []provider.WorkerCeiling) error {
	requested := d.consumer().GetMaxDuration()
	if requested == nil {
		return nil
	}
	i := slices.IndexFunc(ceilings, func(c provider.WorkerCeiling) bool { return c.Compute == compute })
	if i < 0 || ceilings[i].Unbounded || requested.AsDuration() <= ceilings[i].MaxDuration {
		return nil
	}
	return refuse(d, "asks for maxDuration %v, and a worker on %s compute runs at most %v", requested.AsDuration(), compute, ceilings[i].MaxDuration)
}

func refuseWorkerLimits(d declaredResource) error {
	return refuseAtDeclaration(d, provider.RefuseConcurrency(d.Worker.GetConcurrency()))
}

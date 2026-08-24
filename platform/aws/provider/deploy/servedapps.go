package deploy

import (
	"context"
	"sync"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

type servedFunction struct {
	App      string
	Logical  string
	Physical string
	Bytecode *bytecodeConfig
	Warmed   warmReply

	from *release
}

type servedApps struct {
	mu       sync.Mutex
	byLogic  map[string]*servedFunction
	byPhysic map[string]*servedFunction
}

func newServedApps() *servedApps {
	return &servedApps{
		byLogic:  map[string]*servedFunction{},
		byPhysic: map[string]*servedFunction{},
	}
}

func (s *servedApps) plan(from *release, app string, logical []string, bytecode *bytecodeConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range logical {
		s.byLogic[name] = &servedFunction{App: app, Logical: name, Bytecode: bytecode, from: from}
	}
}

func (s *servedApps) realized(logical, physical string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	held, known := s.byLogic[logical]
	if !known || physical == "" {
		return
	}
	held.Physical = physical
	s.byPhysic[physical] = held
}

func (s *servedApps) byPhysicalName(physical string) (*servedFunction, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	held, known := s.byPhysic[physical]
	return held, known
}

func (s *servedApps) warmed(physical string, reply warmReply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if held, known := s.byPhysic[physical]; known {
		held.Warmed = reply
	}
}

func (r *Releaser) Warm(ctx context.Context, targets []string, report providerkit.Reporter) error {
	if !bytecodeCacheEnabled() {
		return nil
	}
	say := sayTo(report)
	warming := map[*release][]warmTarget{}
	for _, physical := range targets {
		held, known := r.served.byPhysicalName(physical)
		if !known || held.Bytecode == nil || held.from == nil || held.from.cfg.Invoker == nil {
			continue
		}
		warming[held.from] = append(warming[held.from],
			warmTarget{App: held.App, LogicalName: held.Logical, FunctionName: physical})
	}
	for from, batch := range warming {
		for _, result := range (warmPass{invoker: from.cfg.Invoker, targets: batch, budget: warmPassDeadline, log: say}).run(ctx) {
			r.served.warmed(result.Target.FunctionName, result.Reply)
		}
	}
	return nil
}

func (r *Releaser) EmbedCode(ctx context.Context, physical string, artifact providerkit.ArtifactRef, report providerkit.Reporter) error {
	if !bytecodeEmbedRequested() {
		return nil
	}
	say := sayTo(report)
	if !bytecodeEmbedEnabled() {
		say("ocel: " + bytecodeEmbedEnv + "=1 has nothing to embed without " + bytecodeCacheEnv + "=1; not embedding")
		return nil
	}
	held, known := r.served.byPhysicalName(physical)
	if !known || held.Bytecode == nil || held.Warmed.Key == "" || held.from == nil {
		return nil
	}
	from := held.from
	if missing := missingEmbedClients(from.cfg); missing != "" {
		say("ocel: " + bytecodeEmbedEnv + "=1 but this deploy has no " + missing + "; not embedding")
		return nil
	}
	code, err := from.artifactAt(artifact)
	if err != nil {
		return nil
	}
	embedPass{
		objects:  from.cfg.Getter,
		uploader: from.cfg.Uploader,
		code:     from.cfg.CodeUpdater,
		invoker:  from.cfg.Invoker,
		targets: []embedTarget{{
			App:          held.App,
			LogicalName:  held.Logical,
			FunctionName: physical,
			Artifact:     code,
			CacheBucket:  held.Bytecode.Bucket,
			CacheKey:     held.Warmed.Key,
			TreeBytes:    held.Warmed.Bytes,
		}},
		budget: embedPassDeadline,
		settle: embedUpdateSettle,
		log:    say,
	}.run(ctx)
	return nil
}

func sayTo(report providerkit.Reporter) func(string) {
	if report == nil {
		return func(string) {}
	}
	return report.Detail
}

package deploy

import (
	"context"
	"sync"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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
	fn, known := s.byLogic[logical]
	if !known || physical == "" {
		return
	}
	fn.Physical = physical
	s.byPhysic[physical] = fn
}

func (s *servedApps) byPhysicalName(physical string) (servedFunction, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn, known := s.byPhysic[physical]
	if !known {
		return servedFunction{}, false
	}
	return *fn, true
}

func (s *servedApps) warmed(physical string, reply warmReply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fn, known := s.byPhysic[physical]; known {
		fn.Warmed = reply
	}
}

func (r *Stacks) Warm(ctx context.Context, targets []string, progress edge.Progress) error {
	if !bytecodeCacheEnabled() {
		return nil
	}
	say := sayTo(progress)
	warming := map[*release][]warmTarget{}
	for _, physical := range targets {
		fn, known := r.served.byPhysicalName(physical)
		if !known || fn.Bytecode == nil || fn.from == nil || fn.from.cfg.Invoker == nil {
			continue
		}
		warming[fn.from] = append(warming[fn.from],
			warmTarget{App: fn.App, LogicalName: fn.Logical, FunctionName: physical})
	}
	for from, batch := range warming {
		for _, result := range (warmPass{invoker: from.cfg.Invoker, targets: batch, budget: warmPassDeadline, log: say}).run(ctx) {
			r.served.warmed(result.Target.FunctionName, result.Reply)
		}
	}
	return nil
}

func (r *Stacks) EmbedCode(ctx context.Context, physical string, artifact provider.ArtifactRef, progress edge.Progress) error {
	if !bytecodeEmbedRequested() {
		return nil
	}
	say := sayTo(progress)
	if !bytecodeEmbedEnabled() {
		say("ocel: " + bytecodeEmbedEnv + "=1 has nothing to embed without " + bytecodeCacheEnv + "=1; not embedding")
		return nil
	}
	fn, known := r.served.byPhysicalName(physical)
	if !known || fn.Bytecode == nil || fn.Warmed.Key == "" || fn.from == nil {
		return nil
	}
	from := fn.from
	if missing := missingEmbedClients(from.cfg); missing != "" {
		say("ocel: " + bytecodeEmbedEnv + "=1 but this deploy has no " + missing + "; not embedding")
		return nil
	}
	code, err := from.artifactAt(artifact)
	if err != nil {
		return nil
	}
	embedPass{
		objects: from.cfg.Getter,
		store:   from.cfg.Objects,
		code:    from.cfg.CodeUpdater,
		invoke:  from.cfg.Invoker,
		targets: []embedTarget{{
			App:          fn.App,
			LogicalName:  fn.Logical,
			FunctionName: physical,
			Artifact:     code,
			CacheBucket:  fn.Bytecode.Bucket,
			CacheKey:     fn.Warmed.Key,
			TreeBytes:    fn.Warmed.Bytes,
		}},
		budget:     embedPassDeadline,
		updateWait: embedUpdateWait,
		log:        say,
	}.run(ctx)
	return nil
}

func sayTo(progress edge.Progress) func(string) {
	if progress == nil {
		return func(string) {}
	}
	return progress.Detail
}

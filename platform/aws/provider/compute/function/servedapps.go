package function

import (
	"context"
	"sync"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/site"
)

type Clients struct {
	Invoker       InvokeAPI
	Getter        ObjectsAPI
	CodeUpdater   FunctionCodeAPI
	Buckets       site.Buckets
	BytecodeEmbed bool
}

type ServedFunction struct {
	App      string
	Logical  string
	Physical string
	Bytecode *Bytecode
	Warmed   warmReply

	from *Clients
}

type ServedApps struct {
	mu       sync.Mutex
	byLogic  map[string]*ServedFunction
	byPhysic map[string]*ServedFunction
}

func NewServedApps() *ServedApps {
	return &ServedApps{
		byLogic:  map[string]*ServedFunction{},
		byPhysic: map[string]*ServedFunction{},
	}
}

func (s *ServedApps) Record(from *Clients, app string, logical []string, bytecode *Bytecode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range logical {
		s.byLogic[name] = &ServedFunction{App: app, Logical: name, Bytecode: bytecode, from: from}
	}
}

func (s *ServedApps) Realize(logical, physical string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn, known := s.byLogic[logical]
	if !known || physical == "" {
		return
	}
	fn.Physical = physical
	s.byPhysic[physical] = fn
}

func (s *ServedApps) Find(physical string) (ServedFunction, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn, known := s.byPhysic[physical]
	if !known {
		return ServedFunction{}, false
	}
	return *fn, true
}

func (s *ServedApps) warmed(physical string, reply warmReply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fn, known := s.byPhysic[physical]; known {
		fn.Warmed = reply
	}
}

func (s *ServedApps) Warm(ctx context.Context, targets []string, progress progress.Log) error {
	warming := map[*Clients][]warmTarget{}
	for _, physical := range targets {
		fn, known := s.Find(physical)
		if !known || fn.Bytecode == nil || fn.from == nil || fn.from.Invoker == nil {
			continue
		}
		warming[fn.from] = append(warming[fn.from],
			warmTarget{App: fn.App, LogicalName: fn.Logical, FunctionName: physical})
	}
	for from, batch := range warming {
		for _, result := range (warmPass{invoker: from.Invoker, targets: batch, bundleDeadline: warmBundleDeadline, progress: progress}).run(ctx) {
			s.warmed(result.Target.FunctionName, result.Reply)
		}
	}
	return nil
}

func (s *ServedApps) EmbedCode(ctx context.Context, physical string, artifact provider.ArtifactRef, progress progress.Log) error {
	fn, known := s.Find(physical)
	if !known || fn.Bytecode == nil || fn.Warmed.Key == "" || fn.from == nil || !fn.from.BytecodeEmbed {
		return nil
	}
	from := fn.from
	if missing := missingEmbedClients(from); missing != "" {
		progress.Warn("No compile cache embedded: this deploy has no " + missing)
		return nil
	}
	code, err := FindArtifact(from.Buckets, artifact)
	if err != nil {
		return nil
	}
	embedPass{
		objects: from.Getter,
		store:   from.Buckets.Objects,
		code:    from.CodeUpdater,
		invoke:  from.Invoker,
		targets: []embedTarget{{
			App:             fn.App,
			LogicalName:     fn.Logical,
			FunctionName:    physical,
			Artifact:        code,
			ArtifactsBucket: fn.Bytecode.Bucket,
			BytecodeKey:     fn.Warmed.Key,
			TreeBytes:       fn.Warmed.Bytes,
		}},
		bundleDeadline: embedBundleDeadline,
		updateWait:     embedUpdateWait,
		progress:       progress,
	}.run(ctx)
	return nil
}

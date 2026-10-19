package bucket

import (
	"reflect"
	"sync"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/container"
)

type Stores struct {
	mu      sync.Mutex
	minted  map[string]storeCredential
	dropped map[string]bool
	sweeps  bool
	shapes  map[string]shapedStore
}

type shapedStore struct {
	spec container.Spec
	by   string
}

func droppedBucket(stack naming.StackName, binding string) string {
	return stack.String() + "/" + binding
}

func (s *Stores) forgot(stack naming.StackName, binding string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dropped == nil {
		s.dropped = map[string]bool{}
	}
	s.dropped[droppedBucket(stack, binding)] = true
}

func (s *Stores) forgotten(stack naming.StackName, binding string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped[droppedBucket(stack, binding)]
}

func (s *Stores) expiring(expiresUploads bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweeps = s.sweeps || !expiresUploads
}

func (s *Stores) sweeping() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sweeps
}

func (s *Stores) shaped(resource string, spec container.Spec) (container.Spec, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	shaped, ok := s.shapes[spec.Name]
	if !ok {
		if s.shapes == nil {
			s.shapes = map[string]shapedStore{}
		}
		s.shapes[spec.Name] = shapedStore{spec: spec, by: resource}
		return spec, nil
	}
	if !reflect.DeepEqual(shaped.spec, spec) {
		return container.Spec{}, refusal.Refuse(refusal.CodeInvalid,
			"buckets %s and %s shape the one store container differently\n"+
				"Gate the `vps.bucket` rule on the environment, or patch both buckets the same way",
			resource, shaped.by)
	}
	return shaped.spec, nil
}

func (s *Stores) shape(name string) *container.Spec {
	s.mu.Lock()
	defer s.mu.Unlock()
	shaped, ok := s.shapes[name]
	if !ok {
		return nil
	}
	return &shaped.spec
}

func (s *Stores) once(name string, mint func() (storeCredential, error)) (storeCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cached, ok := s.minted[name]; ok {
		return cached, nil
	}
	credential, err := mint()
	if err != nil {
		return storeCredential{}, err
	}
	if s.minted == nil {
		s.minted = map[string]storeCredential{}
	}
	s.minted[name] = credential
	return credential, nil
}

package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

const (
	keysFileMaxAge         = 30 * time.Second
	unknownNamespaceReread = time.Second
)

type keySet struct {
	path string
	now  func() time.Time

	mu     sync.Mutex
	keys   map[string]ed25519.PublicKey
	readAt time.Time
}

func newFixedKeySet(source, encoded string) (*keySet, error) {
	keys, err := decodeKeys(source, []byte(encoded))
	if err != nil {
		return nil, err
	}
	return &keySet{keys: keys}, nil
}

func newKeysFileSet(path string, now func() time.Time) (*keySet, error) {
	s := &keySet{path: path, now: now}
	keys, err := s.readFile()
	if err != nil {
		return nil, err
	}
	s.keys, s.readAt = keys, now()
	return s, nil
}

func (s *keySet) find(namespace string) (ed25519.PublicKey, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path != "" {
		_, known := s.keys[namespace]
		age := s.now().Sub(s.readAt)
		if age >= keysFileMaxAge || (!known && age >= unknownNamespaceReread) {
			s.reread()
		}
	}
	key, known := s.keys[namespace]
	return key, known
}

func (s *keySet) reread() {
	s.readAt = s.now()
	keys, err := s.readFile()
	if err != nil {
		fmt.Fprintf(os.Stderr, "keep the realtime keys last read: %v\n", err)
		return
	}
	s.keys = keys
}

func (s *keySet) readFile() (map[string]ed25519.PublicKey, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("read the realtime keys file: %w", err)
	}
	return decodeKeys(s.path, raw)
}

func decodeKeys(source string, raw []byte) (map[string]ed25519.PublicKey, error) {
	var encoded map[string]string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return nil, fmt.Errorf("%s is no JSON object of each namespace's base64 public key: %w", source, err)
	}
	if len(encoded) == 0 {
		return nil, fmt.Errorf("%s names no namespace, so the gateway could verify no token", source)
	}
	keys := make(map[string]ed25519.PublicKey, len(encoded))
	for namespace, key := range encoded {
		decoded, err := base64.StdEncoding.DecodeString(key)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%s names a key for %s that is no base64 Ed25519 public key", source, namespace)
		}
		keys[namespace] = decoded
	}
	return keys, nil
}

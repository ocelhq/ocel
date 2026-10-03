package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

const (
	keysFileMaxAge         = 30 * time.Second
	unknownNamespaceReread = time.Second
)

type keySet struct {
	path string
	now  func() time.Time

	keys      atomic.Pointer[map[string]ed25519.PublicKey]
	readAt    atomic.Int64
	rereading atomic.Bool
}

func newFixedKeySet(source, encoded string) (*keySet, error) {
	keys, err := decodeKeys(source, []byte(encoded))
	if err != nil {
		return nil, err
	}
	s := &keySet{}
	s.keys.Store(&keys)
	return s, nil
}

func newKeysFileSet(path string, now func() time.Time) (*keySet, error) {
	s := &keySet{path: path, now: now}
	keys, err := s.readFile()
	if err != nil {
		return nil, err
	}
	s.keys.Store(&keys)
	s.readAt.Store(now().UnixNano())
	return s, nil
}

func (s *keySet) find(namespace string) (ed25519.PublicKey, bool) {
	keys := *s.keys.Load()
	if s.path != "" {
		_, known := keys[namespace]
		age := time.Duration(s.now().UnixNano() - s.readAt.Load())
		stale := age >= keysFileMaxAge || (!known && age >= unknownNamespaceReread)
		if stale && s.rereading.CompareAndSwap(false, true) {
			keys = s.reread(keys)
			s.rereading.Store(false)
		}
	}
	key, known := keys[namespace]
	return key, known
}

func (s *keySet) reread(current map[string]ed25519.PublicKey) map[string]ed25519.PublicKey {
	s.readAt.Store(s.now().UnixNano())
	keys, err := s.readFile()
	if err != nil {
		fmt.Fprintf(os.Stderr, "keep the realtime keys last read: %v\n", err)
		return current
	}
	s.keys.Store(&keys)
	return keys
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

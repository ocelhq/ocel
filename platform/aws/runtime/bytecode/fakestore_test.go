package bytecode

import (
	"context"
	"io"
	"sync"
)

type fakeStore struct {
	mu      sync.Mutex
	exists  bool
	headErr error
	putErr  error
	heads   []string
	puts    []fakePut

	getBody io.ReadCloser
	getSize int64
	getErr  error
	gets    []string
}

type fakePut struct {
	bucket string
	key    string
	body   []byte
}

func (f *fakeStore) objectExists(_ context.Context, bucket, key string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heads = append(f.heads, bucket+"/"+key)
	return f.exists, f.headErr
}

func (f *fakeStore) putObject(_ context.Context, bucket, key string, body []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = append(f.puts, fakePut{bucket: bucket, key: key, body: body})
	return f.putErr
}

func (f *fakeStore) getObject(_ context.Context, bucket, key string) (io.ReadCloser, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets = append(f.gets, bucket+"/"+key)
	if f.getErr != nil {
		return nil, 0, f.getErr
	}
	return f.getBody, f.getSize, nil
}

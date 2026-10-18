package account

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

const fixtureBucket = "ocel-artifacts-test"

type fakeObjectStore struct {
	mu           sync.Mutex
	objects      map[string][]byte
	checksums    map[string]string
	headModes    []s3types.ChecksumMode
	putChecksums []string
	puts         int
}

func newFakeObjectStore() *fakeObjectStore {
	return &fakeObjectStore{objects: map[string][]byte{}, checksums: map[string]string{}}
}

func (f *fakeObjectStore) putVerified(key string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sum := sha256.Sum256(body)
	f.objects[key] = body
	f.checksums[key] = base64.StdEncoding.EncodeToString(sum[:])
}

func (f *fakeObjectStore) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objects[key]
	return ok
}

func (f *fakeObjectStore) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.headModes = append(f.headModes, in.ChecksumMode)
	if _, ok := f.objects[aws.ToString(in.Key)]; !ok {
		return nil, &s3types.NotFound{}
	}
	out := &s3.HeadObjectOutput{}
	if in.ChecksumMode == s3types.ChecksumModeEnabled {
		if sum, ok := f.checksums[aws.ToString(in.Key)]; ok {
			out.ChecksumSHA256 = aws.String(sum)
		}
	}
	return out, nil
}

func (f *fakeObjectStore) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts++
	f.putChecksums = append(f.putChecksums, aws.ToString(in.ChecksumSHA256))
	body := make([]byte, 0)
	buf := make([]byte, 512)
	for {
		n, err := in.Body.Read(buf)
		body = append(body, buf[:n]...)
		if err != nil {
			break
		}
	}
	key := aws.ToString(in.Key)
	if declared := aws.ToString(in.ChecksumSHA256); declared != "" {
		sum := sha256.Sum256(body)
		if base64.StdEncoding.EncodeToString(sum[:]) != declared {
			return nil, errors.New("BadDigest: the sha256 you specified did not match what we received")
		}
		f.checksums[key] = declared
	}
	f.objects[key] = body
	return &s3.PutObjectOutput{}, nil
}

func bootstrapPayloads() map[string]payloads.Payload {
	byKey := map[string]payloads.Payload{}
	for _, arch := range runtimeArches() {
		p, err := payloads.RuntimeLayer(arch)
		if err != nil {
			panic(err)
		}
		byKey[payloads.Key(runtimeLayerKeyPrefix, p.SHA256)] = p
	}
	return byKey
}

func preloadedStore() *fakeObjectStore {
	store := newFakeObjectStore()
	for key, p := range bootstrapPayloads() {
		store.putVerified(key, p.Bytes)
	}
	return store
}

package payloads

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const (
	fixtureBucket = "ocel-artifacts-test"
	fixturePrefix = "ocel-membrane-layer"
	fixtureLabel  = "membrane layer"
)

type fakeObjectStore struct {
	objects      map[string][]byte
	checksums    map[string]string
	headModes    []s3types.ChecksumMode
	putChecksums []string
	puts         int
}

func newFakeObjectStore() *fakeObjectStore {
	return &fakeObjectStore{objects: map[string][]byte{}, checksums: map[string]string{}}
}

func (f *fakeObjectStore) put(key string, body []byte) {
	f.objects[key] = body
}

func (f *fakeObjectStore) putVerified(key string, body []byte) {
	sum := sha256.Sum256(body)
	f.objects[key] = body
	f.checksums[key] = base64.StdEncoding.EncodeToString(sum[:])
}

func (f *fakeObjectStore) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
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

func placeFixture(store ObjectStore) (Placement, error) {
	return Place(context.Background(), store, fixtureBucket, fixturePrefix, fixtureLabel, MembraneLayer())
}

func TestPlace(t *testing.T) {
	t.Run("uploads a payload the account does not hold", func(t *testing.T) {
		store := newFakeObjectStore()

		at, err := placeFixture(store)
		if err != nil {
			t.Fatalf("Place: %v", err)
		}
		if at.Bucket != fixtureBucket {
			t.Errorf("uploaded into %q, want the account's own artifact bucket", at.Bucket)
		}
		if want := Key(fixturePrefix, MembraneLayer().SHA256); at.Key != want {
			t.Errorf("key = %q, want %q — content-addressed on the embedded digest", at.Key, want)
		}
		if !bytes.Equal(store.objects[at.Key], MembraneLayer().Bytes) {
			t.Error("the account holds bytes other than the embedded payload")
		}
	})

	t.Run("upload sends the digest to S3 as base64", func(t *testing.T) {
		store := newFakeObjectStore()

		if _, err := placeFixture(store); err != nil {
			t.Fatalf("Place: %v", err)
		}
		want := MembraneLayer().ChecksumSHA256
		if len(store.putChecksums) != 1 || store.putChecksums[0] != want {
			t.Errorf("uploaded with checksums %v, want [%s] — base64 of the raw digest, not the hex", store.putChecksums, want)
		}
		for _, got := range store.putChecksums {
			if got == MembraneLayer().SHA256 {
				t.Error("sent the hex digest as ChecksumSHA256; S3 wants base64 of the raw bytes")
			}
		}
	})

	t.Run("skips a payload S3 verified against the digest", func(t *testing.T) {
		store := newFakeObjectStore()
		store.putVerified(Key(fixturePrefix, MembraneLayer().SHA256), MembraneLayer().Bytes)

		at, err := placeFixture(store)
		if err != nil {
			t.Fatalf("Place: %v", err)
		}
		if !at.Present() {
			t.Error("a payload already in the bucket was not used")
		}
		if store.puts != 0 {
			t.Errorf("re-uploaded a payload already present (%d puts)", store.puts)
		}
		if len(store.headModes) != 1 || store.headModes[0] != s3types.ChecksumModeEnabled {
			t.Errorf("head checksum modes %v, want [%s]", store.headModes, s3types.ChecksumModeEnabled)
		}
	})

	t.Run("distrusts bytes at the payload's key", func(t *testing.T) {
		key := Key(fixturePrefix, MembraneLayer().SHA256)
		planted := []byte("MZ\x90\x00 an executable nobody reviewed")

		for _, tc := range []struct {
			name string
			seed func(*fakeObjectStore)
		}{
			{"no stored checksum", func(s *fakeObjectStore) { s.put(key, planted) }},
			{"a checksum of the wrong bytes", func(s *fakeObjectStore) { s.putVerified(key, planted) }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				store := newFakeObjectStore()
				tc.seed(store)

				at, err := placeFixture(store)
				if err != nil {
					t.Fatalf("Place: %v", err)
				}
				if store.puts != 1 {
					t.Errorf("uploaded %d times, want the planted object overwritten once", store.puts)
				}
				if !bytes.Equal(store.objects[at.Key], MembraneLayer().Bytes) {
					t.Error("the account still holds the planted bytes")
				}
			})
		}
	})

	t.Run("refuses without somewhere to put it", func(t *testing.T) {
		if _, err := Place(context.Background(), newFakeObjectStore(), "", fixturePrefix, fixtureLabel, MembraneLayer()); err == nil {
			t.Error("uploaded into no bucket at all")
		}
		if _, err := placeFixture(nil); err == nil {
			t.Error("uploaded through no store at all")
		}
	})

	t.Run("reports a head that failed for another reason", func(t *testing.T) {
		if _, err := placeFixture(headFails{}); err == nil {
			t.Error("treated a broken head as an absent payload")
		}
	})
}

type headFails struct{ ObjectStore }

func (headFails) HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	return nil, errors.New("AccessDenied")
}

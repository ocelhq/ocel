package gcp

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

const cacheStoreBucket = "ocel-edge-cache"

type cacheStoreObject struct {
	body          string
	contentType   string
	authorization string
}

type cacheStoreServer struct {
	mu       sync.Mutex
	objects  map[string]cacheStoreObject
	listings []string
}

func serveCacheStore(t *testing.T) (*cacheStoreServer, string) {
	t.Helper()
	s := &cacheStoreServer{objects: map[string]cacheStoreObject{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if bucket != cacheStoreBucket {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `<Error><Code>NoSuchBucket</Code></Error>`)
			return
		}
		switch {
		case r.Method == http.MethodPut && key != "":
			raw, _ := io.ReadAll(r.Body)
			s.objects[key] = cacheStoreObject{body: string(raw), contentType: r.Header.Get("Content-Type"), authorization: r.Header.Get("Authorization")}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2":
			prefix := r.URL.Query().Get("prefix")
			s.listings = append(s.listings, prefix)
			type content struct {
				Key string `xml:"Key"`
			}
			listing := struct {
				XMLName     xml.Name  `xml:"ListBucketResult"`
				Name        string    `xml:"Name"`
				Prefix      string    `xml:"Prefix"`
				IsTruncated bool      `xml:"IsTruncated"`
				Contents    []content `xml:"Contents"`
			}{Name: bucket, Prefix: prefix}
			for _, name := range slices.Sorted(func(yield func(string) bool) {
				for name := range s.objects {
					if !yield(name) {
						return
					}
				}
			}) {
				if strings.HasPrefix(name, prefix) {
					listing.Contents = append(listing.Contents, content{Key: name})
				}
			}
			_ = xml.NewEncoder(w).Encode(listing)
		case r.Method == http.MethodPost && r.URL.Query().Has("delete"):
			var request struct {
				Objects []struct {
					Key string `xml:"Key"`
				} `xml:"Object"`
			}
			raw, _ := io.ReadAll(r.Body)
			_ = xml.Unmarshal(raw, &request)
			for _, object := range request.Objects {
				delete(s.objects, object.Key)
			}
			_, _ = io.WriteString(w, `<DeleteResult></DeleteResult>`)
		default:
			t.Errorf("the cache store was asked %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotImplemented)
		}
	}))
	t.Cleanup(server.Close)
	return s, server.URL
}

func (s *cacheStoreServer) keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Sorted(func(yield func(string) bool) {
		for name := range s.objects {
			if !yield(name) {
				return
			}
		}
	})
}

func cacheOfferAt(endpoint, accessKeyID, secret string) edge.Offer {
	values := map[string]string{
		edge.OfferKeyBucket:      cacheStoreBucket,
		edge.OfferKeyEndpoint:    endpoint,
		edge.OfferKeyRegion:      "auto",
		edge.OfferKeyAccessKeyID: accessKeyID,
	}
	if secret != "" {
		values[edge.OfferKeySecretAccessKey] = secret
	}
	return edge.Offer{Kind: edge.OfferCacheStore, Values: values}
}

func adoptCacheStoreAt(t *testing.T, h *offersHarness, endpoint string) {
	t.Helper()
	if err := h.adopt([]edge.Offer{storeOffer("c1"), writerOffer("c2"), certificateOffer(), cacheOfferAt(endpoint, "r2-key", "r2-secret")}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRemovingAReleasePrefixClearsItFromTheAdoptedCacheStore(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	store, endpoint := serveCacheStore(t)
	adoptCacheStoreAt(t, h, endpoint)
	store.objects["prod/shop/web/r1a2b3c4d/route-table/ab.json"] = cacheStoreObject{body: "{}"}
	store.objects["prod/shop/web/r1a2b3c4d/isr/page"] = cacheStoreObject{body: "x"}
	store.objects["prod/shop/web/r2b3c4d5e/route-table/cd.json"] = cacheStoreObject{body: "{}"}
	store.objects["prod/other/web/r1a2b3c4d/route-table/ab.json"] = cacheStoreObject{body: "{}"}

	if err := (artifacts{programming(h)}).clearCacheStore(context.Background(), environment.TierProduction, "prod/shop/web/r1a2b3c4d/"); err != nil {
		t.Fatalf("clearCacheStore() = %v", err)
	}

	want := []string{"prod/other/web/r1a2b3c4d/route-table/ab.json", "prod/shop/web/r2b3c4d5e/route-table/cd.json"}
	if got := store.keys(); !slices.Equal(got, want) {
		t.Errorf("the cache store keeps %v, want only %v: the release's prefix is cleared and nothing else", got, want)
	}
	if !slices.Equal(store.listings, []string{"prod/shop/web/r1a2b3c4d/"}) {
		t.Errorf("listed %v, want one listing bounded by the release's prefix", store.listings)
	}
}

func TestRemovingAnEnvironmentPrefixClearsEveryReleaseOfItFromTheAdoptedCacheStore(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	store, endpoint := serveCacheStore(t)
	adoptCacheStoreAt(t, h, endpoint)
	store.objects["pr-7/shop/web/r1a2b3c4d/route-table/ab.json"] = cacheStoreObject{body: "{}"}
	store.objects["pr-7/shop/api/r2b3c4d5e/route-table/cd.json"] = cacheStoreObject{body: "{}"}
	store.objects["prod/shop/web/r1a2b3c4d/route-table/ab.json"] = cacheStoreObject{body: "{}"}

	if err := (artifacts{programming(h)}).clearCacheStore(context.Background(), environment.TierProduction, "pr-7/shop/"); err != nil {
		t.Fatalf("clearCacheStore() = %v", err)
	}

	if got, want := store.keys(), []string{"prod/shop/web/r1a2b3c4d/route-table/ab.json"}; !slices.Equal(got, want) {
		t.Errorf("the cache store keeps %v, want %v", got, want)
	}
}

func TestRemovingAPrefixWhereNoEdgeAdoptedACacheStoreReachesNoStore(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	store, _ := serveCacheStore(t)

	if err := (artifacts{programming(h)}).clearCacheStore(context.Background(), environment.TierProduction, "prod/shop/web/r1a2b3c4d/"); err != nil {
		t.Fatalf("clearCacheStore() = %v, want nothing to do", err)
	}
	if len(store.listings) != 0 {
		t.Errorf("listed %v, want no call", store.listings)
	}
}

func TestRemovingAPrefixFromACacheStoreWhoseBucketIsGoneIsDone(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	_, endpoint := serveCacheStore(t)
	offer := cacheOfferAt(endpoint, "r2-key", "r2-secret")
	offer.Values[edge.OfferKeyBucket] = "deleted-bucket"
	if err := h.adopt([]edge.Offer{storeOffer("c1"), writerOffer("c2"), certificateOffer(), offer}, nil); err != nil {
		t.Fatal(err)
	}

	if err := (artifacts{programming(h)}).clearCacheStore(context.Background(), environment.TierProduction, "prod/shop/"); err != nil {
		t.Errorf("clearCacheStore() = %v, want nothing left to remove", err)
	}
}

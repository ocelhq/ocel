package images_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
)

var aManifestDigest = "sha256:" + strings.Repeat("a", 64)

func answerWithManifest(w http.ResponseWriter) {
	w.Header().Set("Docker-Content-Digest", aManifestDigest)
	w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	w.Header().Set("Content-Length", "100")
}

func TestRemovingATagDeletesTheManifestItNamesByDigest(t *testing.T) {
	var asked []string
	store, push := registryServing(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/" {
			asked = append(asked, r.Method+" "+r.URL.Path)
		}
		switch r.Method {
		case http.MethodHead:
			answerWithManifest(w)
		case http.MethodDelete:
			w.WriteHeader(http.StatusAccepted)
		}
	})

	if err := store.Remove(context.Background(), push.ImageRef); err != nil {
		t.Fatalf("Remove() = %v", err)
	}

	want := []string{
		"HEAD /v2/acme/web/manifests/sha256-abc",
		"DELETE /v2/acme/web/manifests/" + aManifestDigest,
	}
	if !slices.Equal(asked, want) {
		t.Errorf("Remove() asked %v, want %v: a registry deletes by digest, and most refuse a tag", asked, want)
	}
}

func TestRemovingATagTheRegistryDoesNotHaveIsDone(t *testing.T) {
	var deleted bool
	store, push := registryServing(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted = true
		}
		http.Error(w, "manifest unknown", http.StatusNotFound)
	})

	if err := store.Remove(context.Background(), push.ImageRef); err != nil {
		t.Errorf("Remove() = %v, want nothing: an image that is already gone is reclaimed", err)
	}
	if deleted {
		t.Error("Remove() deleted a manifest the registry said it does not have")
	}
}

func TestARegistryThatRefusesDeletesSaysSoAndNamesTheImageItKept(t *testing.T) {
	store, push := registryServing(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			http.Error(w, `{"errors":[{"code":"UNSUPPORTED","message":"The operation is unsupported."}]}`, http.StatusMethodNotAllowed)
			return
		}
		answerWithManifest(w)
	})

	err := store.Remove(context.Background(), push.ImageRef)

	if err == nil {
		t.Fatal("Remove() = nil for a registry that refused the delete, so the image would be reported reclaimed")
	}
	for _, want := range []string{push.ImageRef, "UNSUPPORTED"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Remove() = %q, want it to name %q", err, want)
		}
	}
}

func TestRemovingATagAsksTheRegistryForATokenThatMayDelete(t *testing.T) {
	var bought string
	store, push := registryServing(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			bought = r.URL.RawQuery
			w.Write([]byte(`{"token":"a-scoped-token"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer a-scoped-token" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="http://`+r.Host+`/token",service="registry"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodHead {
			answerWithManifest(w)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})

	if err := store.Remove(context.Background(), push.ImageRef); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	if !strings.Contains(bought, "delete") {
		t.Errorf("the token was bought for %q, want a scope that includes delete", bought)
	}
}

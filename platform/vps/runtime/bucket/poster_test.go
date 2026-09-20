package bucket

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestACallbackIsDeliveredToTheAppTheProxyFronts(t *testing.T) {
	t.Parallel()

	var asked, host string
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked, host = r.URL.RequestURI(), r.Host
	}))
	t.Cleanup(app.Close)

	poster := HTTPPoster{App: app.Listener.Addr().String()}
	if err := poster.Post(context.Background(),
		"https://shop.example.com/api/upload?op=callback", []byte(`{}`)); err != nil {
		t.Fatalf("Post() = %v", err)
	}

	if asked != "/api/upload?op=callback" {
		t.Errorf("the app was asked for %q, want the route the presign named", asked)
	}
	if host != "shop.example.com" {
		t.Errorf("the app was told it is %q, want the hostname the upload was driven against", host)
	}
}

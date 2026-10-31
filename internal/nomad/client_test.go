package nomad

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListRequestsJobMeta(t *testing.T) {
	var gotMeta, gotNamespace string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMeta = r.URL.Query().Get("meta")
		gotNamespace = r.URL.Query().Get("namespace")
		_, _ = w.Write([]byte(`[{"ID":"a","Meta":{"k":"v"}}]`))
	}))
	defer srv.Close()

	c, err := New(Config{Address: srv.URL})
	if err != nil {
		t.Fatalf("creating client: %v", err)
	}

	stubs, err := c.List("apps")
	if err != nil {
		t.Fatalf("listing jobs: %v", err)
	}
	if gotMeta != "true" {
		t.Errorf("expected meta=true query parameter, got %q", gotMeta)
	}
	if gotNamespace != "apps" {
		t.Errorf("expected namespace=apps query parameter, got %q", gotNamespace)
	}
	if len(stubs) != 1 || stubs[0].Meta["k"] != "v" {
		t.Errorf("expected stub meta to be decoded, got %+v", stubs)
	}
}

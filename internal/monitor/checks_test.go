package monitor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPChecks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte(`{"status":{"health":"ok"},"items":[1,2]}`))
	}))
	defer srv.Close()
	cases := []struct {
		cfg  Config
		want bool
	}{
		{Config{URL: srv.URL}, true},
		{Config{URL: srv.URL + "/fail"}, false},
		{Config{URL: srv.URL + "/fail", ExpectStatus: "500-599"}, true},
		{Config{URL: srv.URL, Keyword: "health"}, true},
		{Config{URL: srv.URL, Keyword: "error", InvertKey: true}, true},
		{Config{URL: srv.URL, Keyword: "nope"}, false},
		{Config{URL: srv.URL, JSONPath: "status.health", JSONExpect: "ok"}, true},
		{Config{URL: srv.URL, JSONPath: "items.1", JSONExpect: "2"}, true},
		{Config{URL: srv.URL, JSONPath: "status.health", JSONExpect: "bad"}, false},
	}
	for i, c := range cases {
		if got := Check(context.Background(), "http", c.cfg); got.OK != c.want {
			t.Errorf("case %d: got %v (%s)", i, got.OK, got.Msg)
		}
	}
}

func TestStatusRule(t *testing.T) {
	if !statusOK("200,204", 204) || statusOK("200,204", 201) || !statusOK("", 302) {
		t.Fatal("statusOK")
	}
}

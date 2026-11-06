package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSendFormats(t *testing.T) {
	var got []map[string]any
	var ntfy struct{ title, body, prio string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/updates" {
			ntfy.title, ntfy.body, ntfy.prio = r.Header.Get("Title"), string(b), r.Header.Get("Priority")
			return
		}
		var m map[string]any
		json.Unmarshal(b, &m)
		got = append(got, m)
	}))
	defer srv.Close()
	msg := Message{Title: "OctoPi: 2 security updates", Body: "- a\n- b", Priority: 1}
	for _, typ := range []string{"discord", "webhook"} {
		if err := Send(context.Background(), typ, Config{URL: srv.URL}, msg); err != nil {
			t.Fatal(typ, err)
		}
	}
	if err := Send(context.Background(), "ntfy", Config{URL: srv.URL, Topic: "updates"}, msg); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0]["embeds"] == nil || got[1]["title"] != msg.Title {
		t.Fatalf("payloads: %v", got)
	}
	if ntfy.title != msg.Title || ntfy.prio != "high" || ntfy.body != msg.Body {
		t.Fatalf("ntfy: %+v", ntfy)
	}
}

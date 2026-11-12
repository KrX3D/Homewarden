package integrations

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The test credential is user "root@pam!hw", secret "secret-uuid" (see runFn).

func TestAdGuard(t *testing.T) {
	var updated bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "root@pam!hw" || p != "secret-uuid" {
			w.WriteHeader(401)
			return
		}
		var data any
		switch r.URL.Path {
		case "/control/status":
			data = map[string]any{"version": "v0.107.52", "protection_enabled": false, "running": true}
		case "/control/stats":
			data = map[string]any{"num_dns_queries": 45280, "num_blocked_filtering": 14027, "avg_processing_time": 0.0042, "time_units": "hours", "dns_queries": []int{1, 2, 3}}
		case "/control/filtering/status":
			data = map[string]any{"filters": []map[string]any{{"url": "https://a", "enabled": true, "rules_count": 100}, {"url": "https://b", "enabled": false, "rules_count": 50}},
				"user_rules": []string{"! comment", "||ads.example^", ""}}
		case "/control/version.json":
			data = map[string]any{"new_version": "v0.107.55", "can_autoupdate": true}
		case "/control/update":
			updated = true
			data = map[string]any{}
		default:
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(data)
	}))
	defer srv.Close()
	res := runDetect(t, "adguard", srv, map[string]any{"login": 1})
	f := res["facts"].(map[string]any)
	if f["queries"] != float64(45280) || f["blockedPct"] != float64(31) || f["protection"] != false || f["rules"] != float64(100) || f["customRules"] != float64(1) || f["avgMs"] != 4.2 {
		t.Fatalf("facts: %v", f)
	}
	if w, _ := json.Marshal(f["warnings"]); string(w) != `["adguard_protection_off"]` {
		t.Fatalf("warnings: %s", w)
	}
	c := res["components"].([]any)[0].(map[string]any)
	if c["installed"] != "0.107.52" || c["available"] != "0.107.55" || c["updatable"] != true {
		t.Fatalf("component: %v", c)
	}
	if _, err := runFn(t, "adguard", srv, map[string]any{"login": 1}, "update", []string{"*"}); err != nil || !updated {
		t.Fatalf("update: %v %v", err, updated)
	}
}

func TestNginxProxyManager(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data any
		switch {
		case r.URL.Path == "/api/":
			data = map[string]any{"status": "OK", "version": map[string]any{"major": 2, "minor": 12, "revision": 1}}
		case r.URL.Path == "/api/tokens" && r.Method == "POST":
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			if in["identity"] != "root@pam!hw" || in["secret"] != "secret-uuid" {
				w.WriteHeader(401)
				return
			}
			data = map[string]any{"token": "jwt", "expires": "2030-01-01"}
		case r.Header.Get("Authorization") != "Bearer jwt":
			w.WriteHeader(401)
			return
		case r.URL.Path == "/api/nginx/proxy-hosts":
			data = []map[string]any{{"id": 1, "domain_names": []string{"ha.krx3d.de"}, "enabled": true, "meta": map[string]any{"nginx_online": true}},
				{"id": 2, "domain_names": []string{"old.krx3d.de"}, "enabled": true, "meta": map[string]any{"nginx_online": false}},
				{"id": 3, "domain_names": []string{"off.krx3d.de"}, "enabled": false}}
		case r.URL.Path == "/api/nginx/certificates":
			data = []map[string]any{{"nice_name": "*.krx3d.de", "domain_names": []string{"*.krx3d.de"}, "expires_on": "2020-01-31 10:00:00", "provider": "letsencrypt"}}
		default:
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(data)
	}))
	defer srv.Close()
	res := runDetect(t, "nginx-proxy-manager", srv, map[string]any{"login": 1, "releaseCheck": false})
	f := res["facts"].(map[string]any)
	if f["version"] != "2.12.1" || f["enabledHosts"] != float64(2) || f["offlineHosts"] != float64(1) {
		t.Fatalf("facts: %v", f)
	}
	certs, _ := json.Marshal(f["certs"])
	if !strings.Contains(string(certs), `"expires":1580464800`) {
		t.Fatalf("certs: %s", certs)
	}
	if w, _ := json.Marshal(f["warnings"]); string(w) != `["npm_host_offline","npm_cert_expiring"]` {
		t.Fatalf("warnings: %s", w)
	}
}

func TestGiteaPullRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token secret-uuid" {
			w.WriteHeader(401)
			return
		}
		var data any
		switch r.URL.Path {
		case "/api/v1/version":
			data = map[string]any{"version": "1.22.3"}
		case "/api/v1/repos/issues/search":
			data = []map[string]any{
				{"number": 4, "title": "Update dependency go to v1.27", "user": map[string]any{"login": "renovate-bot"}, "repository": map[string]any{"full_name": "KrX/HomeClaude"}, "html_url": "http://g/1", "created_at": "2026-11-10T10:00:00Z"},
				{"number": 9, "title": "Fix login", "user": map[string]any{"login": "KrX"}, "repository": map[string]any{"full_name": "KrX/MediaWatch"}, "html_url": "http://g/2", "created_at": "2026-11-11T10:00:00Z"},
			}
		default:
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(data)
	}))
	defer srv.Close()
	res := runDetect(t, "gitea", srv, map[string]any{"releaseCheck": false})
	f := res["facts"].(map[string]any)
	prs := f["pullRequests"].([]any)
	if f["openPRs"] != float64(2) || f["botPRs"] != float64(1) || prs[0].(map[string]any)["repo"] != "KrX/MediaWatch" {
		t.Fatalf("facts: %v", f)
	}
}

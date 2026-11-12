package monitor

import (
	"context"
	"os"
	"testing"
)

// Runs against real services when HW_SERVICES_HOST is set (see the PR for the
// containers: postgres :25432, mariadb :23306, redis :26379, mosquitto :21883).
func TestServiceChecks(t *testing.T) {
	host := os.Getenv("HW_SERVICES_HOST")
	if host == "" {
		t.Skip("HW_SERVICES_HOST not set")
	}
	cases := []struct {
		name, typ string
		cfg       Config
		want      bool
	}{
		{"postgres", "postgres", Config{Host: host, Port: 25432, Database: "hwdb", user: "hw", pass: "pgpass"}, true},
		{"postgres wrong password", "postgres", Config{Host: host, Port: 25432, Database: "hwdb", user: "hw", pass: "nope"}, false},
		{"mysql", "mysql", Config{Host: host, Port: 23306, Database: "hwdb", user: "hw", pass: "mypass"}, true},
		{"mysql wrong password", "mysql", Config{Host: host, Port: 23306, user: "hw", pass: "nope"}, false},
		{"redis", "redis", Config{Host: host, Port: 26379, pass: "rdpass"}, true},
		{"redis no auth", "redis", Config{Host: host, Port: 26379}, false},
		{"mqtt", "mqtt", Config{Host: host, Port: 21883, user: "hw", pass: "mqpass"}, true},
		{"mqtt wrong password", "mqtt", Config{Host: host, Port: 21883, user: "hw", pass: "nope"}, false},
		{"mqtt retained topic", "mqtt", Config{Host: host, Port: 21883, user: "hw", pass: "mqpass", Topic: "zigbee2mqtt/bridge/state", Keyword: "online", Timeout: 5}, true},
		{"mqtt keyword missing", "mqtt", Config{Host: host, Port: 21883, user: "hw", pass: "mqpass", Topic: "zigbee2mqtt/bridge/state", Keyword: "offline", Timeout: 3}, false},
		{"mqtt silent topic", "mqtt", Config{Host: host, Port: 21883, user: "hw", pass: "mqpass", Topic: "nothing/here", Timeout: 3}, false},
	}
	for _, c := range cases {
		res := Check(context.Background(), c.typ, c.cfg)
		t.Logf("%-24s ok=%v ms=%.0f %s", c.name, res.OK, res.Ms, res.Msg)
		if res.OK != c.want {
			t.Errorf("%s: ok=%v, want %v (%s)", c.name, res.OK, c.want, res.Msg)
		}
	}
}

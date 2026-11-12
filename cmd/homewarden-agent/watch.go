package main

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/KrX3D/Homewarden/internal/notify/channel"
)

// watchConfig comes with Homewarden's poll answers when this agent is chosen to watch
// it (Settings, Notifications). It is kept in the config file, so an agent restarted
// while Homewarden is down still knows where to send the alert.
type watchConfig struct {
	Type    string            `json:"type"`
	Config  channel.Config    `json:"config"`
	Minutes int               `json:"minutes"`
	Host    string            `json:"host,omitempty"`
	Texts   map[string]string `json:"texts"`
}

// watcher sends one alert when polls have failed for Minutes, and one when they work again.
type watcher struct {
	cfgPath string
	cfg     *config
	lastOK  time.Time
	failing time.Time // first failed poll of the current outage
	alerted bool
}

// update stores a new watch config from a poll answer (nil = no longer watching).
func (w *watcher) update(wc *watchConfig) {
	if reflect.DeepEqual(wc, w.cfg.Watch) {
		return
	}
	w.cfg.Watch = wc
	b, _ := json.MarshalIndent(w.cfg, "", "  ")
	if err := os.WriteFile(w.cfgPath, b, 0o600); err != nil {
		log.Printf("watch: save config: %v", err)
	}
}

func (w *watcher) fill(s, since string) string {
	host, _ := os.Hostname()
	r := strings.NewReplacer("{agent}", host, "{server}", w.cfg.Server, "{since}", since, "{host}", w.cfg.Watch.Host)
	return r.Replace(s)
}

func (w *watcher) send(title, body string, prio int) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := channel.Send(ctx, w.cfg.Watch.Type, w.cfg.Watch.Config, channel.Message{Title: title, Body: body, Priority: prio, Event: "watch"}); err != nil {
		log.Printf("watch: alert not sent: %v", err)
	}
}

// ok is called after a successful poll.
func (w *watcher) ok() {
	if w.alerted && w.cfg.Watch != nil {
		since := w.failing.Format("02.01. 15:04")
		w.send(w.fill(w.cfg.Watch.Texts["upTitle"], since), w.fill(w.cfg.Watch.Texts["upBody"], since), 0)
	}
	w.lastOK, w.failing, w.alerted = time.Now(), time.Time{}, false
}

// failed is called after a failed poll.
func (w *watcher) failed() {
	if w.failing.IsZero() {
		w.failing = time.Now()
	}
	wc := w.cfg.Watch
	if wc == nil || w.alerted || time.Since(w.failing) < time.Duration(max(wc.Minutes, 1))*time.Minute {
		return
	}
	w.alerted = true
	since := w.failing.Format("02.01. 15:04")
	body := w.fill(wc.Texts["downBody"], since)
	if wc.Host != "" {
		c, err := net.DialTimeout("tcp", wc.Host, 5*time.Second)
		if err == nil {
			c.Close()
			body += "\n" + w.fill(wc.Texts["hostUp"], since)
		} else {
			body += "\n" + w.fill(wc.Texts["hostDown"], since)
		}
	}
	log.Printf("watch: Homewarden unreachable since %s, sending alert", since)
	w.send(w.fill(wc.Texts["downTitle"], since), body, 1)
}

// Command homewarden-agent runs on a device and executes commands Homewarden sends.
//
// It connects out to Homewarden over HTTP(S) and long-polls for work, so the device
// needs no open port and Homewarden needs no route to it. Commands run as the user the
// agent runs as (root via the installer), like an SSH login as root would.
//
// First start:  homewarden-agent -server https://homewarden.lan -token <enroll token>
// Afterwards:   homewarden-agent            (reads the saved config)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var version = "dev"

type config struct {
	Server   string `json:"server"`
	Key      string `json:"key"`
	DeviceID int64  `json:"deviceId"`
	Insecure bool   `json:"insecure,omitempty"`
	// Watch: alert through this channel when Homewarden is unreachable (see watch.go).
	Watch *watchConfig `json:"watch,omitempty"`
}

func defaultConfigPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("ProgramData"), "Homewarden", "agent.json")
	}
	return "/etc/homewarden-agent.json"
}

type command struct {
	ID      string `json:"id"`
	Cmd     string `json:"cmd"`
	Stdin   string `json:"stdin"`
	Timeout int    `json:"timeout"`
}

type result struct {
	ID     string `json:"id"`
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Code   int    `json:"code"`
	Error  string `json:"error,omitempty"`
}

type hello struct {
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Version  string `json:"version"`
}

func me() hello {
	h, _ := os.Hostname()
	return hello{Hostname: h, OS: runtime.GOOS, Arch: runtime.GOARCH, Version: version}
}

func post(client *http.Client, url, key string, in, out any) error {
	b, _ := json.Marshal(in)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Agent "+key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return errUnauthorized
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

var errUnauthorized = errors.New("Homewarden refused this agent (revoked or wrong key)")

func run(c command) result {
	timeout := time.Duration(c.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		// PowerShell writes in the console code page by default; force UTF-8 so names
		// with umlauts survive, and so localized table headers stay parseable.
		cmd = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
			"[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; $OutputEncoding = [System.Text.Encoding]::UTF8; "+c.Cmd)
	} else {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", c.Cmd)
	}
	if runtime.GOOS == "windows" {
		// Windows PowerShell 5.1 must build its own module path. Inheriting PowerShell 7's
		// (when the agent was started from pwsh) makes built-in modules such as
		// Microsoft.PowerShell.Security fail to load.
		env := []string{}
		for _, e := range os.Environ() {
			if !strings.HasPrefix(strings.ToUpper(e), "PSMODULEPATH=") {
				env = append(env, e)
			}
		}
		cmd.Env = env
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if c.Stdin != "" {
		cmd.Stdin = strings.NewReader(c.Stdin)
	}
	err := cmd.Run()
	r := result{ID: c.ID, Stdout: out.String(), Stderr: errb.String()}
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		r.Code = exit.ExitCode()
	case ctx.Err() != nil:
		r.Code, r.Error = -1, "timed out after "+timeout.String()
	default:
		r.Code, r.Error = -1, err.Error()
	}
	return r
}

func main() {
	cfgPath := flag.String("config", defaultConfigPath(), "config file")
	server := flag.String("server", "", "Homewarden URL (first start)")
	token := flag.String("token", "", "enrollment token (first start)")
	insecure := flag.Bool("insecure", false, "accept a self-signed HTTPS certificate")
	showVersion := flag.Bool("version", false, "print the version")
	enrollOnly := flag.Bool("enroll-only", false, "enroll, save the config and exit (used by the installers)")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	var cfg config
	if b, err := os.ReadFile(*cfgPath); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}
	client := newClient(cfg.Insecure || *insecure, 0)

	if *token != "" {
		if *server == "" {
			log.Fatal("-server is required with -token")
		}
		base := strings.TrimRight(*server, "/")
		var out struct {
			Key      string `json:"key"`
			DeviceID int64  `json:"deviceId"`
		}
		in := struct {
			hello
			Token string `json:"token"`
		}{me(), *token}
		if err := post(client, base+"/api/agent/enroll", "", in, &out); err != nil {
			log.Fatalf("enrollment failed: %v", err)
		}
		cfg = config{Server: base, Key: out.Key, DeviceID: out.DeviceID, Insecure: *insecure}
		b, _ := json.MarshalIndent(cfg, "", "  ")
		if err := os.MkdirAll(filepath.Dir(*cfgPath), 0o700); err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(*cfgPath, b, 0o600); err != nil {
			log.Fatalf("save config: %v", err)
		}
		log.Printf("enrolled as device %d, config saved to %s", cfg.DeviceID, *cfgPath)
		if *enrollOnly {
			return
		}
	}
	if cfg.Server == "" || cfg.Key == "" {
		log.Fatalf("not enrolled: run once with -server and -token (config %s)", *cfgPath)
	}

	pollClient := newClient(cfg.Insecure, 60*time.Second)
	sem := make(chan struct{}, 4)
	backoff := time.Second
	watch := &watcher{cfgPath: *cfgPath, cfg: &cfg, lastOK: time.Now()}
	log.Printf("homewarden-agent %s connected to %s", version, cfg.Server)
	for {
		var resp struct {
			Commands []command    `json:"commands"`
			Watch    *watchConfig `json:"watch"`
		}
		err := post(pollClient, cfg.Server+"/api/agent/poll", cfg.Key, me(), &resp)
		if errors.Is(err, errUnauthorized) {
			log.Fatal(err)
		}
		if err != nil {
			log.Printf("poll: %v (retrying in %s)", err, backoff)
			watch.failed()
			time.Sleep(backoff)
			backoff = min(backoff*2, time.Minute)
			continue
		}
		backoff = time.Second
		watch.ok()
		watch.update(resp.Watch)
		for _, c := range resp.Commands {
			sem <- struct{}{}
			go func(c command) {
				defer func() { <-sem }()
				r := run(c)
				for i := 0; i < 3; i++ {
					if err := post(client, cfg.Server+"/api/agent/result", cfg.Key, r, nil); err == nil {
						return
					}
					time.Sleep(2 * time.Second)
				}
			}(c)
		}
	}
}

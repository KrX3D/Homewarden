package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/KrX3D/Homewarden/internal/agent"
	"github.com/KrX3D/Homewarden/internal/auth"
	"github.com/KrX3D/Homewarden/internal/conn"
	"github.com/KrX3D/Homewarden/internal/sshx"
	"github.com/KrX3D/Homewarden/internal/store"
)

// AgentBinDir holds the agent binaries in the image (homewarden-agent-<os>-<arch>).
var AgentBinDir = "/usr/local/share/homewarden/agent"

// AttachAgents registers enrollment, polling and the agent downloads.
func (s *Server) AttachAgents(h *agent.Hub) {
	s.agents = h
	m := s.mux
	m.Handle("POST /api/devices/{id}/agent/enroll", s.authed(auth.PermDevicesManage, s.startEnroll))
	m.Handle("POST /api/devices/{id}/agent/install-ssh", s.authed(auth.PermDevicesManage, s.installAgentSSH))
	m.Handle("GET /api/devices/{id}/agent", s.authed(auth.PermDevicesView, s.agentStatus))
	m.Handle("DELETE /api/devices/{id}/agent", s.authed(auth.PermDevicesManage, s.revokeAgent))
	m.HandleFunc("POST /api/agent/enroll", s.agentEnroll)
	m.HandleFunc("POST /api/agent/poll", s.agentPoll)
	m.HandleFunc("POST /api/agent/result", s.agentResult)
	m.HandleFunc("GET /agent/install.sh", s.agentInstallSh)
	m.HandleFunc("GET /agent/install.ps1", s.agentInstallPs1)
	m.HandleFunc("GET /agent/download/{target}", s.agentDownload)
}

func (s *Server) startEnroll(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	token := auth.NewSetupToken()
	exp := time.Now().Add(24 * time.Hour)
	if err := s.Store.StartEnrollment(r.Context(), d.ID, auth.HashToken(token), exp.Unix()); err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "expires": exp.Unix()})
}

// installAgentSSH installs the Linux agent from the GUI: it logs in over SSH (with a
// password typed in for this once, or the device's saved connection), runs the same
// installer as the copy-paste command and lets the agent enrol itself.
func (s *Server) installAgentSSH(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	var in struct {
		Server   string `json:"server"` // Homewarden's address as the device reaches it
		Insecure bool   `json:"insecure"`
		User     string `json:"user"`
		Password string `json:"password"`
		Port     int    `json:"port"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(in.Server), "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.ContainsAny(in.Server, " '\"\n") {
		FailMsg(w, http.StatusBadRequest, "bad_request", "server must be an http(s) address")
		return
	}
	if d.Host == "" {
		FailMsg(w, http.StatusBadRequest, "bad_request", "the device has no address")
		return
	}
	token := auth.NewSetupToken()
	if err := s.Store.StartEnrollment(r.Context(), d.ID, auth.HashToken(token), time.Now().Add(30*time.Minute).Unix()); err != nil {
		internal(w, err)
		return
	}
	args := []string{u.String(), token}
	if in.Insecure {
		args = append(args, "--insecure")
	}
	out, err := s.conn.RunScriptAsRoot(r.Context(), d, conn.Login{User: strings.TrimSpace(in.User), Password: in.Password, Port: in.Port}, installSh, args, 4*time.Minute)
	if err != nil {
		var mismatch *sshx.HostKeyMismatch
		if errors.Is(err, conn.ErrNotConfigured) || errors.As(err, &mismatch) || out == "" {
			sshError(w, err)
			return
		}
		writeJSON(w, http.StatusBadGateway, APIError{Error: "install_failed", Message: err.Error(), Detail: map[string]string{"output": tail(out, 4000)}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": tail(out, 4000)})
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

func (s *Server) agentStatus(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	a, err := s.Store.AgentFor(r.Context(), d.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"enrolled": false, "online": false})
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enrolled": a.Enrolled, "online": s.agents.Online(d.ID), "agent": a, "pendingEnrollment": a.EnrollExpires > time.Now().Unix()})
}

func (s *Server) revokeAgent(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteAgent(r.Context(), d.ID); err != nil {
		internal(w, err)
		return
	}
	s.agents.Forget(d.ID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type agentHello struct {
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Version  string `json:"version"`
	Token    string `json:"token"`
}

func (s *Server) agentEnroll(w http.ResponseWriter, r *http.Request) {
	var in agentHello
	if !readJSON(w, r, &in) {
		return
	}
	ip := "agent:" + clientIP(r)
	if d := s.limiter.Blocked(ip); d > 0 {
		fail(w, http.StatusTooManyRequests, "rate_limited")
		return
	}
	a, err := s.Store.AgentByEnroll(r.Context(), auth.HashToken(strings.ToUpper(strings.TrimSpace(in.Token))))
	if err != nil {
		s.limiter.Fail(ip)
		fail(w, http.StatusForbidden, "invalid_token")
		return
	}
	raw, _ := auth.NewToken()
	key := "hwa_" + raw
	if err := s.Store.CompleteEnrollment(r.Context(), a.DeviceID, auth.HashToken(key), in.Hostname, in.OS, in.Arch, in.Version); err != nil {
		internal(w, err)
		return
	}
	// Route the device's commands through the agent from now on.
	if d, err := s.Store.Device(r.Context(), a.DeviceID); err == nil {
		set := conn.FromDevice(d)
		set.Method = "agent"
		if cfg, err := conn.WithSettings(d, set); err == nil {
			d.Config = cfg
			_ = s.Store.UpdateDevice(r.Context(), d)
		}
		s.dev.Events.Publish("device.changed", map[string]int64{"id": d.ID})
	}
	s.limiter.Success(ip)
	writeJSON(w, http.StatusOK, map[string]any{"key": key, "deviceId": a.DeviceID})
}

func (s *Server) agentAuth(r *http.Request) (store.Agent, bool) {
	key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Agent ")
	if !ok {
		return store.Agent{}, false
	}
	a, err := s.Store.AgentByKey(r.Context(), auth.HashToken(strings.TrimSpace(key)))
	return a, err == nil
}

func (s *Server) agentPoll(w http.ResponseWriter, r *http.Request) {
	a, ok := s.agentAuth(r)
	if !ok {
		fail(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var in agentHello
	if !readJSON(w, r, &in) {
		return
	}
	wasOnline := s.agents.Online(a.DeviceID)
	_ = s.Store.TouchAgent(r.Context(), a.DeviceID, in.Hostname, in.OS, in.Arch, in.Version)
	if !wasOnline {
		s.dev.Presence.Poke()
	}
	cmds := s.agents.Poll(r.Context(), a.DeviceID, 25*time.Second)
	if cmds == nil {
		cmds = []agent.Command{}
	}
	out := map[string]any{"commands": cmds}
	// A watching agent gets its alert channel with every answer, so it still has it
	// when Homewarden goes away.
	if s.notify != nil {
		if wc := s.notify.WatchFor(r.Context(), a.DeviceID); wc != nil {
			out["watch"] = wc
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) agentResult(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.agentAuth(r); !ok {
		fail(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var in agent.Result
	if !readJSON(w, r, &in) {
		return
	}
	s.agents.Deliver(in)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

var targetRe = regexp.MustCompile(`^(linux|windows)-(amd64|arm64|arm)$`)

func (s *Server) agentDownload(w http.ResponseWriter, r *http.Request) {
	t := r.PathValue("target")
	if !targetRe.MatchString(t) {
		http.NotFound(w, r)
		return
	}
	name := "homewarden-agent-" + t
	if strings.HasPrefix(t, "windows") {
		name += ".exe"
	}
	p := filepath.Join(AgentBinDir, name)
	if _, err := os.Stat(p); err != nil {
		http.Error(w, "this image has no agent build for "+t, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeFile(w, r, p)
}

func (s *Server) agentInstallSh(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	fmt.Fprint(w, installSh)
}

func (s *Server) agentInstallPs1(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, installPs1)
}

// installSh installs the agent as a systemd service (or with OpenRC/rc.local where
// systemd is missing, e.g. Unraid).
const installSh = `#!/bin/sh
# Homewarden agent installer. Usage: curl -fsSL <server>/agent/install.sh | sh -s -- <server> <token> [--insecure]
set -e
SERVER="$1"; TOKEN="$2"; EXTRA="$3"
[ -n "$SERVER" ] && [ -n "$TOKEN" ] || { echo "usage: install.sh <server> <token> [--insecure]"; exit 1; }
[ "$(id -u)" = 0 ] || { echo "run as root (sudo sh)"; exit 1; }
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  armv7l|armv6l|armhf) ARCH=arm ;;
  *) echo "unsupported architecture $(uname -m)"; exit 1 ;;
esac
K=""; [ "$EXTRA" = "--insecure" ] && K="-k"
BIN=/usr/local/bin/homewarden-agent
[ -d /boot/config ] && [ -f /etc/unraid-version ] && BIN=/boot/config/homewarden-agent
echo "Downloading agent for linux-$ARCH"
if command -v curl >/dev/null 2>&1; then
  curl -fsSL $K -o "$BIN.new" "$SERVER/agent/download/linux-$ARCH"
elif command -v wget >/dev/null 2>&1; then
  W=""; [ "$EXTRA" = "--insecure" ] && W="--no-check-certificate"
  wget -q $W -O "$BIN.new" "$SERVER/agent/download/linux-$ARCH"
else
  echo "needs curl or wget to download the agent"; exit 1
fi
chmod 755 "$BIN.new" && mv "$BIN.new" "$BIN"
INS=""; [ "$EXTRA" = "--insecure" ] && INS="-insecure"
CONF=/etc/homewarden-agent.json
[ -f /etc/unraid-version ] && CONF=/boot/config/homewarden-agent.json
"$BIN" -config "$CONF" -server "$SERVER" -token "$TOKEN" $INS -enroll-only
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  cat > /etc/systemd/system/homewarden-agent.service <<EOF
[Unit]
Description=Homewarden agent
After=network-online.target
Wants=network-online.target
[Service]
ExecStart=$BIN -config $CONF
Restart=always
RestartSec=10
[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable --now homewarden-agent
  echo "Installed as systemd service homewarden-agent."
elif [ -f /etc/unraid-version ]; then
  grep -q homewarden-agent /boot/config/go || echo "nohup $BIN -config $CONF >/var/log/homewarden-agent.log 2>&1 &" >> /boot/config/go
  nohup "$BIN" -config "$CONF" >/var/log/homewarden-agent.log 2>&1 &
  echo "Installed on Unraid: started now and from /boot/config/go at every boot."
else
  nohup "$BIN" -config "$CONF" >/var/log/homewarden-agent.log 2>&1 &
  echo "Started. No systemd found: add '$BIN -config $CONF &' to your startup scripts."
fi
`

const installPs1 = `# Homewarden agent installer for Windows. Run in an elevated PowerShell:
#   & ([scriptblock]::Create((irm <server>/agent/install.ps1))) <server> <token>
param([Parameter(Mandatory)][string]$Server, [Parameter(Mandatory)][string]$Token)
$ErrorActionPreference = 'Stop'
$dir = Join-Path $env:ProgramFiles 'Homewarden'
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$bin = Join-Path $dir 'homewarden-agent.exe'
Invoke-WebRequest -UseBasicParsing -Uri "$Server/agent/download/windows-amd64" -OutFile $bin
$p = Start-Process -FilePath $bin -ArgumentList @('-server', $Server, '-token', $Token, '-enroll-only') -Wait -PassThru -NoNewWindow
if ($p.ExitCode -ne 0) { throw 'Enrollment failed. Create a new install command in Homewarden and try again.' }
$action = New-ScheduledTaskAction -Execute $bin
$trigger = New-ScheduledTaskTrigger -AtStartup
$settings = New-ScheduledTaskSettingsSet -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero)
Register-ScheduledTask -TaskName 'Homewarden agent' -Action $action -Trigger $trigger -Settings $settings -User 'SYSTEM' -RunLevel Highest -Force | Out-Null
Start-ScheduledTask -TaskName 'Homewarden agent'
Write-Host 'Homewarden agent installed as scheduled task "Homewarden agent" (runs as SYSTEM at startup).'
`

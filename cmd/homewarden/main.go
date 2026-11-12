// Command homewarden is the Homewarden server.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	builtin "github.com/KrX3D/Homewarden/integrations"
	"github.com/KrX3D/Homewarden/internal/agent"
	"github.com/KrX3D/Homewarden/internal/api"
	"github.com/KrX3D/Homewarden/internal/automation"
	"github.com/KrX3D/Homewarden/internal/conn"
	"github.com/KrX3D/Homewarden/internal/discovery"
	"github.com/KrX3D/Homewarden/internal/events"
	"github.com/KrX3D/Homewarden/internal/integrations"
	"github.com/KrX3D/Homewarden/internal/jobs"
	"github.com/KrX3D/Homewarden/internal/monitor"
	"github.com/KrX3D/Homewarden/internal/notify"
	"github.com/KrX3D/Homewarden/internal/presence"
	"github.com/KrX3D/Homewarden/internal/store"
	"github.com/KrX3D/Homewarden/internal/vault"
	"github.com/KrX3D/Homewarden/internal/web"
)

// Set by the image build via -ldflags.
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	addr := env("HOMEWARDEN_ADDR", ":8080")
	dataDir := env("HOMEWARDEN_DATA", "/data")

	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		log.Fatalf("data directory %s: %v", dataDir, err)
	}
	v, err := vault.Load(dataDir)
	if err != nil {
		log.Fatalf("master key: %v", err)
	}
	st, err := store.Open(filepath.Join(dataDir, "homewarden.db"))
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := api.New(st, v, api.BuildInfo{Version: version, Commit: commit, BuildDate: buildDate}, web.Handler())

	broker := events.New()
	engine := presence.New(st, broker)
	srv.AttachDevices(api.DeviceDeps{Events: broker, Presence: engine, Scanner: discovery.New(ctx, st, broker)})
	hub := agent.NewHub()
	engine.AgentOnline = hub.Online
	connSvc := &conn.Service{St: st, Vault: v, Agents: hub}
	srv.AttachCredentials(connSvc)
	srv.AttachAgents(hub)

	reg := integrations.NewRegistry(dataDir, st, builtin.FS)
	if err := reg.Load(ctx); err != nil {
		log.Fatalf("integrations: %v", err)
	}
	runner := jobs.New(st, broker)
	sw := &jobs.Software{St: st, Reg: reg, Host: &integrations.Host{St: st, Vault: v, Conn: connSvc}, Runner: runner, Events: broker}
	sw.Register()
	sw.RegisterBackup()
	sw.RegisterRestore()
	runner.Start(ctx, 4)
	engine.OnReady(sw.OnReady)
	go sw.AutoScan(ctx)
	go sw.MetricsLoop(ctx)
	srv.AttachSoftware(api.SoftwareDeps{Registry: reg, Runner: runner, Software: sw})
	go srv.CatalogLoop(ctx) // integration updates: at start and every 6 hours
	srv.AttachBackups()

	auto := &automation.Service{St: st, Sw: sw}
	engine.OnReady(auto.OnReady)
	go auto.Run(ctx)
	mq := &automation.MQTT{St: st, Vault: v, Events: broker, Auto: auto}
	mq.Reload(ctx)
	srv.SetBaseContext(ctx)
	srv.AttachAutomation(auto, mq)

	notifier := &notify.Service{St: st, Vault: v, Events: broker}
	go notifier.Run(ctx)
	srv.AttachNotify(notifier)

	monRunner := monitor.NewRunner(st, broker)
	go monRunner.Run(ctx)
	srv.AttachMonitors(monRunner)
	srv.AttachOverview()

	go engine.Run(ctx)

	token, err := srv.PrepareSetup(ctx)
	if err != nil {
		log.Fatalf("setup: %v", err)
	}
	if token != "" {
		log.Printf("========================================================")
		log.Printf("No account exists yet. Open the web interface and enter")
		log.Printf("this setup token to create the admin account:")
		log.Printf("")
		log.Printf("    %s", token)
		log.Printf("")
		log.Printf("The token changes on every restart until setup is done.")
		log.Printf("========================================================")
	}

	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = st.PurgeSessions(ctx)
			}
		}
	}()

	httpSrv := &http.Server{Addr: addr, Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Printf("homewarden %s (%s, built %s) listening on %s, data in %s", version, commit, buildDate, addr, dataDir)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

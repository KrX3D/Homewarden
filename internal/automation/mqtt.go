package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/KrX3D/Homewarden/internal/events"
	"github.com/KrX3D/Homewarden/internal/store"
	"github.com/KrX3D/Homewarden/internal/vault"
)

// MQTTSettings are stored under the "mqtt" setting.
type MQTTSettings struct {
	Enabled      bool   `json:"enabled"`
	Broker       string `json:"broker"` // tcp://host:1883
	CredentialID int64  `json:"credentialId,omitempty"`
	BaseTopic    string `json:"baseTopic"`
}

// MQTT connects Homewarden to a broker: it publishes device states and update counts
// and accepts commands.
//
//	<base>/status                       online | offline (retained, last will)
//	<base>/device/<slug>/state          ready | reachable | offline ... (retained)
//	<base>/device/<slug>/updates        number of pending updates (retained)
//	<base>/device/<slug>/cmd            publish: scan | backup | update | update security
type MQTT struct {
	St     *store.Store
	Vault  *vault.Vault
	Events *events.Broker
	Auto   *Service

	mu     sync.Mutex
	client mqtt.Client
	cfg    MQTTSettings
	status string
	stop   chan struct{}
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slug turns a device name into a topic segment ("OctoPi Left" -> "octopi_left").
func Slug(name string) string {
	return strings.Trim(slugRe.ReplaceAllString(strings.ToLower(name), "_"), "_")
}

// LoadSettings reads the MQTT settings.
func LoadSettings(ctx context.Context, st *store.Store) MQTTSettings {
	raw, _ := st.GetSetting(ctx, "mqtt", "")
	var s MQTTSettings
	_ = json.Unmarshal([]byte(raw), &s)
	if s.BaseTopic == "" {
		s.BaseTopic = "homewarden"
	}
	return s
}

// Status describes the connection for the settings page.
func (m *MQTT) Status() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *MQTT) setStatus(s string) {
	m.mu.Lock()
	m.status = s
	m.mu.Unlock()
}

// Reload (re)connects with the stored settings.
func (m *MQTT) Reload(ctx context.Context) {
	m.mu.Lock()
	if m.client != nil {
		close(m.stop)
		m.client.Publish(m.cfg.BaseTopic+"/status", 1, true, "offline").WaitTimeout(2 * time.Second)
		m.client.Disconnect(500)
		m.client = nil
	}
	m.mu.Unlock()
	cfg := LoadSettings(ctx, m.St)
	if !cfg.Enabled || cfg.Broker == "" {
		m.setStatus("off")
		return
	}
	opts := mqtt.NewClientOptions().AddBroker(cfg.Broker).SetClientID("homewarden").
		SetAutoReconnect(true).SetConnectRetry(true).SetConnectRetryInterval(10*time.Second).
		SetWill(cfg.BaseTopic+"/status", "offline", 1, true)
	if cfg.CredentialID != 0 {
		if c, err := m.St.Credential(ctx, cfg.CredentialID); err == nil {
			if pw, err := m.Vault.DecryptString(c.Secret); err == nil {
				opts.SetUsername(c.Username).SetPassword(pw)
			}
		}
	}
	opts.SetOnConnectHandler(func(c mqtt.Client) {
		m.setStatus("connected")
		c.Publish(cfg.BaseTopic+"/status", 1, true, "online")
		c.Subscribe(cfg.BaseTopic+"/device/+/cmd", 1, func(_ mqtt.Client, msg mqtt.Message) { m.command(ctx, cfg, msg) })
		go m.publishAll(ctx, c, cfg)
	})
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) { m.setStatus("lost: " + err.Error()) })
	client := mqtt.NewClient(opts)
	stop := make(chan struct{})
	m.mu.Lock()
	m.client, m.cfg, m.stop = client, cfg, stop
	m.mu.Unlock()
	m.setStatus("connecting")
	client.Connect()
	go m.forward(ctx, client, cfg, stop)
}

func (m *MQTT) publishAll(ctx context.Context, c mqtt.Client, cfg MQTTSettings) {
	devs, err := m.St.Devices(ctx, store.StatusActive)
	if err != nil {
		return
	}
	counts, _ := m.St.UpdateCounts(ctx)
	for _, d := range devs {
		base := cfg.BaseTopic + "/device/" + Slug(d.Name)
		c.Publish(base+"/state", 1, true, d.State)
		c.Publish(base+"/updates", 1, true, strconv.Itoa(counts[d.ID][0]))
	}
}

// forward republishes device events while connected.
func (m *MQTT) forward(ctx context.Context, c mqtt.Client, cfg MQTTSettings, stop chan struct{}) {
	ch := m.Events.Subscribe()
	defer m.Events.Unsubscribe(ch)
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case ev := <-ch:
			if ev.Type != "device.state" && ev.Type != "device.software" {
				continue
			}
			b, _ := json.Marshal(ev.Data)
			var x struct {
				ID    int64  `json:"id"`
				State string `json:"state"`
			}
			_ = json.Unmarshal(b, &x)
			d, err := m.St.Device(ctx, x.ID)
			if err != nil || d.Status != store.StatusActive {
				continue
			}
			base := cfg.BaseTopic + "/device/" + Slug(d.Name)
			if ev.Type == "device.state" {
				c.Publish(base+"/state", 1, true, x.State)
			} else {
				counts, _ := m.St.UpdateCounts(ctx)
				c.Publish(base+"/updates", 1, true, strconv.Itoa(counts[d.ID][0]))
			}
		}
	}
}

func (m *MQTT) command(ctx context.Context, cfg MQTTSettings, msg mqtt.Message) {
	parts := strings.Split(strings.TrimPrefix(msg.Topic(), cfg.BaseTopic+"/device/"), "/")
	if len(parts) != 2 {
		return
	}
	slug, payload := parts[0], strings.ToLower(strings.TrimSpace(string(msg.Payload())))
	devs, err := m.St.Devices(ctx, store.StatusActive)
	if err != nil {
		return
	}
	for _, d := range devs {
		if Slug(d.Name) != slug && strconv.FormatInt(d.ID, 10) != slug {
			continue
		}
		action, scope := payload, ""
		switch payload {
		case "update", "update all":
			action, scope = "update", "all"
		case "update security":
			action, scope = "update", "security"
		case "scan", "backup":
		default:
			log.Printf("mqtt: unknown command %q for %s", payload, d.Name)
			return
		}
		r, err := m.Auto.Trigger(ctx, d, action, scope, "mqtt")
		if err != nil {
			log.Printf("mqtt: %s %s: %v", action, d.Name, err)
			return
		}
		log.Printf("mqtt: %s %s: %s", action, d.Name, r)
		return
	}
	log.Printf("mqtt: no device %q", slug)
}

// Describe is shown in the settings page.
func Describe(cfg MQTTSettings) string {
	return fmt.Sprintf("%s/device/<device>/cmd", cfg.BaseTopic)
}

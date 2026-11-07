package store

import (
	"context"
	"encoding/json"
)

// IntegrationDefaults are settings shared by all devices of one integration. A device
// uses them wherever it has no value of its own, so credentials and options are set up
// once per integration instead of once per device.
type IntegrationDefaults struct {
	// Settings fall back key by key: a device setting that is missing or empty takes
	// the default.
	Settings map[string]any `json:"settings"`
	// Connection is used as a whole by devices that have no credential of their own
	// and do not use the agent.
	Connection map[string]any `json:"connection,omitempty"`
}

func defaultsKey(id string) string { return "integration.defaults." + id }

// IntegrationDefaults returns the shared settings of an integration (empty when unset).
func (s *Store) IntegrationDefaults(ctx context.Context, id string) (IntegrationDefaults, error) {
	out := IntegrationDefaults{Settings: map[string]any{}}
	v, err := s.GetSetting(ctx, defaultsKey(id), "")
	if err != nil || v == "" {
		return out, err
	}
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return IntegrationDefaults{Settings: map[string]any{}}, err
	}
	if out.Settings == nil {
		out.Settings = map[string]any{}
	}
	return out, nil
}

// SetIntegrationDefaults stores the shared settings of an integration.
func (s *Store) SetIntegrationDefaults(ctx context.Context, id string, d IntegrationDefaults) error {
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return s.SetSetting(ctx, defaultsKey(id), string(b))
}

// empty reports values that count as "not set" in a settings form.
func empty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	}
	return false
}

// WithDefaults returns d with its integration's defaults filled in. The result is
// for reading only: never save it, or the defaults would be frozen into the device.
func (s *Store) WithDefaults(ctx context.Context, d Device) Device {
	if d.Kind == "" || d.Kind == "generic" {
		return d
	}
	def, err := s.IntegrationDefaults(ctx, d.Kind)
	if err != nil || (len(def.Settings) == 0 && len(def.Connection) == 0) {
		return d
	}
	cfg := map[string]any{}
	if len(d.Config) > 0 {
		if err := json.Unmarshal(d.Config, &cfg); err != nil {
			return d
		}
	}
	if len(def.Settings) > 0 {
		set, _ := cfg["settings"].(map[string]any)
		if set == nil {
			set = map[string]any{}
		}
		for k, v := range def.Settings {
			if cur, ok := set[k]; !ok || empty(cur) {
				set[k] = v
			}
		}
		cfg["settings"] = set
	}
	if cid, _ := def.Connection["credentialId"].(float64); cid != 0 {
		c, _ := cfg["connection"].(map[string]any)
		method, _ := c["method"].(string)
		own, _ := c["credentialId"].(float64)
		if method != "agent" && own == 0 {
			merged := map[string]any{}
			for k, v := range def.Connection {
				merged[k] = v
			}
			merged["method"] = "ssh"
			cfg["connection"] = merged
		}
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return d
	}
	d.Config = b
	return d
}

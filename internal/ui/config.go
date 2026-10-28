package ui

import (
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
	"time"
)

// Settings contains only saved nonsecret choices. Ports contains overrides only.
type Settings struct {
	Discovery bool     `json:"discovery"`
	Version   int      `json:"version"`
	Bind      string   `json:"bind"`
	Idle      string   `json:"idle"`
	Serial    string   `json:"serial"`
	Model     string   `json:"model"`
	Ports     []string `json:"ports"`
	Email     string   `json:"email"`
	Remember  bool     `json:"remember_password"`
}

func defaultSettings() Settings {
	return Settings{Version: 1, Bind: "0.0.0.0", Discovery: true, Idle: "0", Ports: []string{"80:80"}}
}
func (s Settings) validate() ([]proxyrun.Port, time.Duration, error) {
	idle, err := time.ParseDuration(s.Idle)
	if s.Version != 1 {
		return nil, 0, &ActionError{Code: "settings"}
	}
	if err != nil || idle < 0 {
		return nil, 0, &ActionError{Code: "idle", Field: "idle"}
	}
	if s.Discovery && !landiscovery.ValidBind(s.Bind) {
		return nil, 0, &ActionError{Code: "discovery_bind", Field: "bind"}
	}
	if s.Serial != "" && !cloudhttp.ValidSerial(s.Serial) {
		return nil, 0, &ActionError{Code: "serial", Field: "serial"}
	}
	if s.Model != "" && !cloudhttp.ValidModel(s.Model) {
		return nil, 0, &ActionError{Code: "model", Field: "model"}
	}
	if s.Email != "" {
		if _, err := cloud.EmailCredentials(s.Email, "validation-only"); err != nil {
			return nil, 0, &ActionError{Code: "email", Field: "email"}
		}
	}
	var ports []proxyrun.Port
	for _, p := range s.Ports {
		ports, err = proxyrun.ParsePort(ports, p)
		if err != nil {
			return nil, 0, &ActionError{Code: "ports", Field: "ports"}
		}
	}
	ports, err = proxyrun.MergePorts(ports)
	if err != nil || len(ports) == 0 {
		return nil, 0, &ActionError{Code: "ports", Field: "ports"}
	}
	return ports, idle, nil
}
func loadSettings(s *storage) (Settings, error) {
	settings := Settings{Bind: "127.0.0.1", Idle: "0", Ports: []string{}}
	settings.Version = 0
	// Decode a fresh struct: absent members have documented defaults; unknown
	// members are accepted by JSON v2 within this schema version.
	if err := s.read("config.json", &settings); err != nil {
		return Settings{}, err
	}
	if _, _, err := settings.validate(); err != nil {
		return Settings{}, err
	}
	return settings, nil
}

// LoadSettings reads the UI's saved settings without writing files or taking ui.lock.
// The root is also the installation identity root for a headless run.
func LoadSettings(root string) (Settings, error) {
	d, err := privatefile.ReadDirectory(root)
	if err != nil {
		return Settings{}, errStorage
	}
	defer d.Close()
	return loadSettings(&storage{root: root, directory: d})
}

func (s Settings) lazy() bool { d, _ := time.ParseDuration(s.Idle); return d > 0 }

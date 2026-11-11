package ui

import (
	"encoding/json/jsontext"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
	"os"
	"strings"
	"time"
)

// Settings contains only saved nonsecret choices. Ports contains overrides only.
type Settings struct {
	Discovery bool            `json:"discovery"`
	Version   int             `json:"version"`
	Bind      string          `json:"bind"`
	Idle      string          `json:"idle"`
	Serial    string          `json:"serial"`
	Model     string          `json:"model"`
	Ports     []proxyrun.Port `json:"ports"`
	Email     string          `json:"email"`
	Remember  bool            `json:"remember_password"`
	// Route is how the copier reaches the telescope: routeLAN, or the
	// account route for any other value, including an older file's none.
	Route      string `json:"route,omitempty"`
	LANAddress string `json:"lan_address,omitempty"`
	// LANSerial is the serial number a scan saw at LANAddress when it was
	// saved, or "". A connect scans for it, when LANScan says to, and uses
	// the address that answers with it, LANAddress first. It is never
	// shown or logged.
	LANSerial string `json:"lan_serial,omitempty"`
	// LANScan is when a connect to LANSerial scans: "start", "connect" or
	// "off" (see [landiscovery.Mode]). Empty, or any other value, is
	// start; a value this version doesn't know is kept until changed.
	LANScan string `json:"lan_scan,omitempty"`
	// RouteEstablished opens Copy, Browse and Free up space at launch: the
	// account route has connected, or an address on this network passed
	// Test and was saved. Only those two saves set it; every other save
	// carries the last saved value, and nothing clears it.
	RouteEstablished bool           `json:"route_established,omitempty"`
	Unknown          jsontext.Value `json:",embed"`
}

// The routes to the telescope.
const (
	routeAccount = "account"
	routeLAN     = "lan"
)

func defaultSettings() Settings {
	return Settings{Version: 1, Bind: "0.0.0.0", Discovery: true, Idle: "0", Ports: []proxyrun.Port{{Local: 80, Device: 80}}}
}
func (s Settings) validate() ([]proxyrun.Port, time.Duration, error) {
	idle, err := time.ParseDuration(s.Idle)
	if s.Version != 1 {
		return nil, 0, &ActionError{Code: "settings"}
	}
	if err != nil || idle < 0 {
		return nil, 0, &ActionError{Code: "idle", Field: "idle"}
	}
	if strings.TrimSpace(s.Bind) == "" {
		return nil, 0, &ActionError{Code: "bind_empty", Field: "bind"}
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
	// Ports holds standard-service overrides and added ports alike, under
	// the rules of -p.
	var overrides []proxyrun.Port
	for _, p := range s.Ports {
		if overrides, err = proxyrun.AddPort(overrides, p); err != nil {
			return nil, 0, &ActionError{Code: "ports", Field: "ports"}
		}
	}
	ports, err := proxyrun.MergePorts(overrides)
	if err != nil {
		return nil, 0, &ActionError{Code: "ports", Field: "ports"}
	}
	if len(ports) == 0 {
		return nil, 0, &ActionError{Code: "ports_empty", Field: "ports"}
	}
	return ports, idle, nil
}

// defaultBind is config.json's bind when the member is absent.
const defaultBind = "127.0.0.1"

func loadSettings(s *storage) (Settings, []diagnostic.Fallback, error) {
	settings := Settings{Bind: defaultBind, Idle: "0", Ports: []proxyrun.Port{}}
	// Decode a fresh struct: absent members have documented defaults; unknown
	// members are retained by JSON v2 within this schema version.
	if err := s.read("config.json", &settings); err != nil {
		if !os.IsNotExist(err) {
			s.settingsRefused = true
		}
		return Settings{}, nil, err
	}
	fallbacks := settings.fallBack()
	if _, _, err := settings.validate(); err != nil {
		s.settingsRefused = true
		return Settings{}, nil, err
	}
	s.settingsUnknown = settings
	return settings, fallbacks, nil
}

// fallBack replaces the saved values a load tolerates, so that one bad
// value doesn't refuse the whole file. It leaves the version and the ports
// for validate to refuse.
func (s *Settings) fallBack() []diagnostic.Fallback {
	var fallbacks []diagnostic.Fallback
	replace := func(setting, fallback string) {
		fallbacks = append(fallbacks, diagnostic.Fallback{File: "config.json", Setting: setting, Fallback: fallback})
	}
	if idle, err := time.ParseDuration(s.Idle); err != nil || idle < 0 {
		s.Idle = "0"
		replace("idle", "0")
	}
	if strings.TrimSpace(s.Bind) == "" {
		s.Bind = defaultBind
		replace("bind", defaultBind)
	}
	if s.Discovery && !landiscovery.ValidBind(s.Bind) {
		s.Discovery = false
		replace("discovery", "off")
	}
	if s.Serial != "" && !cloudhttp.ValidSerial(s.Serial) {
		s.Serial = ""
		replace("serial", "none")
	}
	if s.Model != "" && !cloudhttp.ValidModel(s.Model) {
		s.Model = ""
		replace("model", "none")
	}
	if s.Email != "" {
		if _, err := cloud.EmailCredentials(s.Email, "validation-only"); err != nil {
			s.Email = ""
			replace("email", "none")
		}
	}
	return fallbacks
}

// LoadSettings reads the UI's saved settings without writing files or taking ui.lock.
// The root is also the installation identity root for a headless run. The
// fallbacks are the saved values it replaced, for the caller to log.
func LoadSettings(root string) (Settings, []diagnostic.Fallback, error) {
	d, err := privatefile.ReadDirectory(root)
	if err != nil {
		return Settings{}, nil, errStorage
	}
	defer d.Close()
	return loadSettings(&storage{root: root, directory: d})
}

func (s Settings) lazy() bool { d, _ := time.ParseDuration(s.Idle); return d > 0 }

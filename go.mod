module github.com/zmerlynn/seestar-wormhole

go 1.27

require (
	github.com/pion/dtls/v3 v3.1.15
	github.com/pion/logging v0.2.4
	github.com/pion/transport/v5 v5.0.0
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.0.10
	github.com/zalando/go-keyring v0.2.8
	go.yaml.in/yaml/v3 v3.0.5
	golang.org/x/net v0.60.0
	golang.org/x/sys v0.48.0
)

require (
	github.com/danieljoos/wincred v1.2.3 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	golang.org/x/crypto v0.57.0 // indirect
)

replace golang.org/x/net => github.com/zmerlynn/net v0.60.1-wormhole.3

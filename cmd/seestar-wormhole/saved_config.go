package main

import (
	"context"
	"errors"
	"time"

	"github.com/spf13/pflag"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/ui"
)

const savedPasswordRule = "Password unavailable: set SEESTAR_PASSWORD or save a password in the OS keychain using the UI"

func loadProxySettings(o *options, flags *pflag.FlagSet) error {
	root, err := ui.StateRoot(o.stateDir)
	if err != nil {
		return &argumentError{Option: "--config", Rule: "use the UI's private state directory with saved config.json"}
	}
	s, err := ui.LoadSettings(root)
	if err != nil {
		return &argumentError{Option: "--config", Rule: "use a valid saved config.json in the UI's private state directory"}
	}
	o.stateDir, o.savedEmail = root, s.Email
	if !flags.Changed("bind") {
		o.bind = s.Bind
	}
	if !flags.Changed("idle") {
		o.idle, _ = time.ParseDuration(s.Idle)
	}
	if !flags.Changed("serial") {
		o.serial = s.Serial
	}
	if !flags.Changed("model") {
		o.model = s.Model
	}
	// CLI overrides replace the saved override for the same device port.
	// Drop replaced entries before validating the merged local-port uniqueness.
	for _, mapping := range s.Ports {
		var saved mappings
		if err := saved.Set(mapping); err != nil {
			return err
		}
		replaced := false
		for _, explicit := range o.ports {
			if explicit.Device == saved[0].Device {
				replaced = true
				break
			}
		}
		if !replaced {
			if err := o.ports.Set(mapping); err != nil {
				return err
			}
		}
	}
	return nil
}

func savedCredentials(ctx context.Context, email string, lookup func(string) (string, bool), getPassword func(context.Context, string) (string, error)) (cloud.Credentials, error) {
	if value, ok := lookup("SEESTAR_EMAIL"); ok {
		email = value
	}
	password, set := lookup("SEESTAR_PASSWORD")
	if !set {
		var err error
		password, err = getPassword(ctx, email)
		if err != nil || password == "" {
			return cloud.Credentials{}, errors.Join(cloud.ErrConfig, &argumentError{Option: "--config", Rule: savedPasswordRule})
		}
	}
	credentials, err := cloud.EmailCredentials(email, password)
	if err != nil {
		return cloud.Credentials{}, errors.Join(err, &argumentError{Option: "--config", Rule: "use a valid email and password in saved settings, environment, or OS keychain"})
	}
	return credentials, nil
}

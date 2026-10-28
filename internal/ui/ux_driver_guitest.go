//go:build gui && guitest

package ui

import (
	"os"
	"path/filepath"
	"strconv"
)

func (d *guiTestDriver) js(script string) { d.w.ExecJS(`(()=>{` + script + `})()`) }
func (d *guiTestDriver) uxSetup() error {
	switch d.scenario {
	case "computer":
		d.js(`const e=document.getElementById('goal');e.value='computer';e.dispatchEvent(new Event('change'));`)
		if err := d.probe(`!document.getElementById('discovery').checked && document.getElementById('listen').value==='127.0.0.1' && document.getElementById('trust').hidden`); err != nil {
			return err
		}
		if err := d.state("Not connected", false, "computer-only"); err != nil {
			return err
		}
	case "incompatible":
		d.js(`document.getElementById('advanced').open=true;const e=document.getElementById('listen');e.value='127.0.0.1';e.dispatchEvent(new Event('change'));`)
		if err := d.click("start"); err != nil {
			return err
		}
		if err := d.probe(`document.getElementById('bind-error').textContent.includes('Phone discovery cannot use this address.') && !document.getElementById('bind-error').hidden`); err != nil {
			return err
		}
		if err := d.state("Not connected", false, "discovery-incompatible"); err != nil {
			return err
		}
		d.js(`document.getElementById('listen').value='0.0.0.0';document.getElementById('listen').dispatchEvent(new Event('change'));`)
	case "browser-copy":
		if err := d.probe(`document.getElementById('quit').textContent==='Quit service' && document.getElementById('quit-help').textContent.startsWith('Closing this tab leaves the proxy running.')`); err != nil {
			return err
		}
	case "settings-failure":
		// Fail only the synthetic state's config write, after initial loading.
		if err := os.Mkdir(filepath.Join(d.root, "config.json"), 0700); err != nil {
			return err
		}
	}
	switch d.scenario {
	case "saved-password", "save-denied", "save-timeout", "remove-denied", "remove-timeout":
		if err := d.click("save-password"); err != nil {
			return err
		}
		if d.scenario == "save-denied" || d.scenario == "save-timeout" {
			if err := d.probe(`!document.getElementById('notice').hidden && !document.getElementById('save-password').disabled`); err != nil {
				return err
			}
			if err := d.state("Not connected", false, d.scenario); err != nil {
				return err
			}
			if err := d.click("save-password"); err != nil {
				return err
			}
		}
		if err := d.probe(`document.getElementById('password').readOnly && document.getElementById('password').value==='Saved in keychain' && !document.getElementById('forget').disabled`); err != nil {
			return err
		}
		if err := d.state("Not connected", false, "password-saved"); err != nil {
			return err
		}
		if d.scenario == "remove-denied" || d.scenario == "remove-timeout" {
			failures := 1
			if d.scenario == "remove-denied" {
				failures = 2
			}
			for i := 0; i < failures; i++ {
				if err := d.click("forget"); err != nil {
					return err
				}
				if err := d.probe(`!document.getElementById('notice').hidden && !document.getElementById('forget').disabled`); err != nil {
					return err
				}
				if err := d.state("Not connected", false, "removal-retry-"+strconv.Itoa(i+1)); err != nil {
					return err
				}
			}
			if err := d.click("forget"); err != nil {
				return err
			}
			if err := d.probe(`!document.getElementById('password').readOnly && document.getElementById('forget').hidden && document.getElementById('feedback').textContent==='Password removed from keychain and this app.'`); err != nil {
				return err
			}
			if err := d.state("Not connected", false, "password-removed"); err != nil {
				return err
			}
			d.js(`const e=document.getElementById('password');e.value='synthetic-password';e.dispatchEvent(new Event('input'));`)
			// Save/remove again on the same DOM: catches a latched own-disabled flag.
			if err := d.click("save-password"); err != nil {
				return err
			}
			if err := d.probe(`!document.getElementById('forget').hidden && !document.getElementById('forget').disabled`); err != nil {
				return err
			}
			if err := d.click("forget"); err != nil {
				return err
			}
			if err := d.probe(`document.getElementById('forget').hidden`); err != nil {
				return err
			}
			d.js(`const e=document.getElementById('password');e.value='synthetic-password';e.dispatchEvent(new Event('input'));`)
		}
	}
	return nil
}
func (d *guiTestDriver) uxRunning() error {
	switch d.scenario {
	case "settings-failure":
		if err := d.probe(`!document.getElementById('retry-storage').hidden && document.getElementById('notice').textContent.includes('Settings could not be saved')`); err != nil {
			return err
		}
		if err := d.state("Connected to your telescope", true, "settings-storage-failed"); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(d.root, "config.json")); err != nil {
			return err
		}
		if err := d.click("retry-storage"); err != nil {
			return err
		}
		if err := d.probe(`document.getElementById('retry-storage').hidden && document.getElementById('saved-state').textContent==='Settings saved.'`); err != nil {
			return err
		}
	case "details":
		d.js(`document.getElementById('details').open=true;`)
		if err := d.probe(`document.getElementById('events').textContent.includes('Connected to your telescope') && document.getElementById('log-path').textContent.includes('gui.log')`); err != nil {
			return err
		}
		if err := d.state("Connected to your telescope", true, "details-expanded"); err != nil {
			return err
		}
	case "lost-controls":
		d.js(`token='synthetic-invalid-token';`)
		if err := d.probe(`document.getElementById('notice').textContent.startsWith('Connection to the controls was lost.') && document.getElementById('last-known').textContent==='Last known state: Connected to your telescope' && document.getElementById('start').disabled`); err != nil {
			return err
		}
		if err := d.capture(d.ctx, filepath.Join(d.dir, "lost-controls.png")); err != nil {
			return err
		}
		// Restore only the fixture's own ephemeral capability.
		d.js(`token=sessionStorage.getItem('seestar-control-token');`)
		if err := d.probe(`!document.getElementById('start').disabled && document.getElementById('last-known').hidden`); err != nil {
			return err
		}
		return d.runnerUninterrupted()
	}
	return nil
}

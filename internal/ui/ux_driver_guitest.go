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
		if err := d.probe(`check(!el('discovery').checked && el('listen').value==='127.0.0.1' && el('trust').hidden,{discovery:el('discovery').checked,listen:el('listen').value,trustHidden:el('trust').hidden})`); err != nil {
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
		if err := d.probe(`check(el('bind-error').textContent.includes('Phone discovery cannot use this address.') && !el('bind-error').hidden,{bindError:el('bind-error').textContent,hidden:el('bind-error').hidden})`); err != nil {
			return err
		}
		if err := d.state("Not connected", false, "discovery-incompatible"); err != nil {
			return err
		}
		d.js(`document.getElementById('listen').value='0.0.0.0';document.getElementById('listen').dispatchEvent(new Event('change'));`)
	case "browser-copy":
		if err := d.probe(`check(el('quit').textContent==='Quit service' && el('quit-help').textContent.startsWith('Closing this tab leaves the proxy running.'),{quit:el('quit').textContent,help:el('quit-help').textContent})`); err != nil {
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
			if err := d.probe(`check(!el('notice').hidden && !el('save-password').disabled,{noticeHidden:el('notice').hidden,saveDisabled:el('save-password').disabled})`); err != nil {
				return err
			}
			if err := d.state("Not connected", false, d.scenario); err != nil {
				return err
			}
			if err := d.click("save-password"); err != nil {
				return err
			}
		}
		if err := d.probe(`check(el('password').readOnly && el('password').value==='Saved in keychain' && !el('forget').disabled,{readOnly:el('password').readOnly,savedPlaceholder:el('password').value==='Saved in keychain',forgetDisabled:el('forget').disabled})`); err != nil {
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
				if err := d.probe(`check(!el('notice').hidden && !el('forget').disabled,{noticeHidden:el('notice').hidden,forgetDisabled:el('forget').disabled})`); err != nil {
					return err
				}
				if err := d.state("Not connected", false, "removal-retry-"+strconv.Itoa(i+1)); err != nil {
					return err
				}
			}
			if err := d.click("forget"); err != nil {
				return err
			}
			if err := d.probe(`check(!el('password').readOnly && el('forget').hidden && el('feedback').textContent==='Password removed from keychain and this app.' && el('password-error').hidden && !el('password').hasAttribute('aria-invalid'),{readOnly:el('password').readOnly,forgetHidden:el('forget').hidden,feedback:el('feedback').textContent,errorHidden:el('password-error').hidden,invalid:el('password').getAttribute('aria-invalid')})`); err != nil {
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
			if err := d.probe(`check(!el('forget').hidden && !el('forget').disabled,{forgetHidden:el('forget').hidden,forgetDisabled:el('forget').disabled})`); err != nil {
				return err
			}
			if err := d.click("forget"); err != nil {
				return err
			}
			if err := d.probe(`check(el('forget').hidden,{forgetHidden:el('forget').hidden})`); err != nil {
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
		if err := d.probe(`check(!el('retry-storage').hidden && el('notice').textContent.includes('Settings could not be saved'),{retryHidden:el('retry-storage').hidden,notice:el('notice').textContent})`); err != nil {
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
		if err := d.probe(`check(el('retry-storage').hidden && el('notice').hidden,{retryHidden:el('retry-storage').hidden,noticeHidden:el('notice').hidden})`); err != nil {
			return err
		}
	case "details":
		d.js(`document.getElementById('details').open=true;`)
		if err := d.probe(`check(el('events').textContent.includes('Connected to your telescope') && el('log-path').textContent.includes('gui.log'),{events:el('events').textContent,logPath:el('log-path').textContent})`); err != nil {
			return err
		}
		if err := d.state("Connected to your telescope", true, "details-expanded"); err != nil {
			return err
		}
	case "lost-controls":
		d.js(`token='synthetic-invalid-token';`)
		if err := d.probe(`check(el('notice').textContent.startsWith('Connection to the controls was lost.') && el('last-known').textContent==='Last known state: Connected to your telescope' && el('start').disabled,{notice:el('notice').textContent,lastKnown:el('last-known').textContent,startDisabled:el('start').disabled})`); err != nil {
			return err
		}
		if err := d.capture(d.ctx, filepath.Join(d.dir, "lost-controls.png")); err != nil {
			return err
		}
		// Restore only the fixture's own ephemeral capability.
		d.js(`token=sessionStorage.getItem('seestar-control-token');`)
		if err := d.probe(`check(!el('start').disabled && el('last-known').hidden,{startDisabled:el('start').disabled,lastKnownHidden:el('last-known').hidden})`); err != nil {
			return err
		}
		return d.runnerUninterrupted()
	}
	return nil
}

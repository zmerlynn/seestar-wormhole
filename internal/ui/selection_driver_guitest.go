//go:build gui && guitest

package ui

import "github.com/zmerlynn/seestar-wormhole/internal/session"

func (d *guiTestDriver) telescopePicker() error {
	if err := d.start(); err != nil {
		return err
	}
	if err := d.probe(`check(!el('device-picker').hidden && el('device-choice').options.length===3 && el('device-choice').options[1].textContent==='Seestar S50 Pro synthetic-choice-1234' && el('device-choice').options[2].textContent==='Future telescope\\tmodel synthetic-choice-5678' && el('serial').value==='' && el('model').value==='' && el('change-telescope').hidden,{pickerHidden:el('device-picker').hidden,options:el('device-choice').options.length})`); err != nil {
		return err
	}
	if err := d.state("Could not connect", false, "telescope-picker"); err != nil {
		return err
	}
	// Choose the unknown model: display escaping must never become the wire value.
	d.js(`const e=document.getElementById('device-choice');e.value='1';e.dispatchEvent(new Event('change',{bubbles:true}));`)
	if err := d.probe(`check(el('advanced').open && el('serial').value==='synthetic-choice-5678' && el('model').value==='Future telescope\tmodel' && el('feedback').textContent==='Telescope selection filled in Advanced. Choose Connect to try again.',{advanced:el('advanced').open,serial:el('serial').value,model:el('model').value})`); err != nil {
		return err
	}
	if err := d.state("Could not connect", false, "telescope-chosen"); err != nil {
		return err
	}
	if err := d.start(); err != nil {
		return err
	}
	if err := d.step(session.Event{Kind: session.Progress}); err != nil {
		return err
	}
	if err := d.probe(`check(el('selected-device').textContent==='Telescope: Future telescope\\tmodel ***5678' && !el('selected-device').hidden && !el('change-telescope').hidden && !el('change-telescope').disabled,{selected:el('selected-device').textContent,changeHidden:el('change-telescope').hidden})`); err != nil {
		return err
	}
	if err := d.state("Connected to your Seestar", true, "telescope-selected"); err != nil {
		return err
	}
	if err := d.click("change-telescope"); err != nil {
		return err
	}
	if err := d.release(); err != nil {
		return err
	}
	if err := d.probe(`check(el('serial').value==='' && el('model').value==='' && el('selected-device').hidden && el('change-telescope').hidden && !el('start').disabled && el('start').textContent==='Connect',{serial:el('serial').value,model:el('model').value,changeHidden:el('change-telescope').hidden})`); err != nil {
		return err
	}
	if err := d.state("Stopped. Seestar may still be releasing the session.", false, "telescope-stopped"); err != nil {
		return err
	}
	if err := d.state("Stopped. Seestar may still be releasing the session.", false, "telescope-cleared"); err != nil {
		return err
	}
	// The next Connect must enumerate again from the cleared saved pair.
	if err := d.start(); err != nil {
		return err
	}
	if err := d.probe(`check(!el('device-picker').hidden && el('device-choice').options.length===3 && el('device-choice').value==='',{pickerHidden:el('device-picker').hidden,choice:el('device-choice').value})`); err != nil {
		return err
	}
	if err := d.state("Could not connect", false, "telescope-picker-again"); err != nil {
		return err
	}
	return d.click("quit")
}

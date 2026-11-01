//go:build gui && guitest

package ui

import "github.com/zmerlynn/seestar-wormhole/internal/session"

func (d *guiTestDriver) telescopePicker() error {
	if err := d.probe(guiTestDiscoveryPickerProbe); err != nil {
		return err
	}
	if err := d.start(); err != nil {
		return err
	}
	if err := d.probe(`check(!el('device-picker').hidden && el('device-choice').options.length===4 && el('device-choice').options[1].textContent==='Seestar S50 Pro synthetic-choice-1234' && el('device-choice').options[2].textContent==='Future telescope\\tmodel synthetic-choice-5678' && el('serial').value==='' && el('model').value==='' && el('device-choice').options[0].textContent==='Choose a telescope…' && el('device-choice').options[3].textContent==='Enter manually…' && el('serial-error').hidden && el('error-code').hidden && el('selection-help').textContent==='Your account has several telescopes. Choose one, then Connect.' && el('device-choice').getAttribute('aria-describedby')==='selection-help' && document.activeElement===el('device-choice'),{pickerHidden:el('device-picker').hidden,options:el('device-choice').options.length})`); err != nil {
		return err
	}
	if err := d.probe(guiTestSelectionPickerProbe); err != nil {
		return err
	}
	if err := d.state("Choose a telescope", false, "telescope-picker"); err != nil {
		return err
	}
	d.js(`const picker=document.getElementById('device-choice');picker.value='custom';picker.dispatchEvent(new Event('change',{bubbles:true}));`)
	if err := d.probe(guiTestManualPickerProbe); err != nil {
		return err
	}
	if err := d.probe(guiTestManualGapProbe); err != nil {
		return err
	}
	if err := d.state("Enter a serial number.", false, "telescope-manual"); err != nil {
		return err
	}
	// Choose the unknown model: display escaping must never become the wire value.
	d.js(`const e=document.getElementById('device-choice');e.value='1';e.dispatchEvent(new Event('change',{bubbles:true}));`)
	if err := d.probe(`check(!el('advanced').open && el('custom-device').hidden && el('serial').value==='synthetic-choice-5678' && el('model').value==='Future telescope\tmodel' && el('status').textContent==='Ready to connect',{advanced:el('advanced').open,serial:el('serial').value,model:el('model').value})`); err != nil {
		return err
	}
	if err := d.state("Ready to connect", false, "telescope-chosen"); err != nil {
		return err
	}
	// Connect a known model for the connected-row capture.
	d.js(`const e=document.getElementById('device-choice');e.value='0';e.dispatchEvent(new Event('change',{bubbles:true}));`)
	if err := d.start(); err != nil {
		return err
	}
	if err := d.step(session.Event{Kind: session.Progress}); err != nil {
		return err
	}
	if err := d.probe(guiTestConnectedTelescopeProbe); err != nil {
		return err
	}
	if err := d.state("Connected to Seestar S50 Pro synthetic-choice-1234", true, "telescope-selected"); err != nil {
		return err
	}
	if err := d.click("start"); err != nil {
		return err
	}
	if err := d.release(); err != nil {
		return err
	}
	if err := d.state("Stopped. Seestar may still be releasing the session.", false, "telescope-stopped"); err != nil {
		return err
	}
	// Stop retains the saved pair. Find on my account requests a new selection.
	d.js(`const e=document.getElementById('device-choice');e.value='';e.dispatchEvent(new Event('change',{bubbles:true}));`)
	if err := d.probe(guiTestDiscoveryPickerProbe); err != nil {
		return err
	}
	if err := d.start(); err != nil {
		return err
	}
	if err := d.probe(guiTestSelectionPickerProbe); err != nil {
		return err
	}
	if err := d.state("Choose a telescope", false, "telescope-picker-again"); err != nil {
		return err
	}
	// Empty Connect keeps the received choices without starting another lookup.
	if err := d.click("start"); err != nil {
		return err
	}
	if err := d.probe(guiTestSelectionPickerProbe); err != nil {
		return err
	}

	return d.click("quit")
}

const guiTestDiscoveryPickerProbe = `check(el('device-choice').children[0].textContent==='Find on my account' && !el('selection-help').hidden && el('selection-help').textContent==='Connect uses your only telescope, or asks which one if there are several.' && el('device-choice').getAttribute('aria-describedby')==='selection-help',{})`

const guiTestManualPickerProbe = `(() => {
 const picker=el('device-choice'),manual=el('custom-device'),action=el('picker-action'),siblings=Array.from(manual.parentElement.children);
 return check(picker.children[picker.children.length-1].textContent==='Enter manually…' && !manual.hidden && siblings.indexOf(manual)<siblings.indexOf(action) && el('start').parentElement===action && document.activeElement===picker && el('status').textContent==='Enter a serial number.' && !el('serial-help').hidden && el('serial-help').textContent==='Enter a serial number.' && el('serial').getAttribute('aria-describedby').split(' ').includes('serial-help'),{});
})()`

const guiTestSelectionPickerProbe = `check(el('device-choice').value==='' && el('device-choice').children[0].textContent==='Choose a telescope…' && el('status').textContent==='Choose a telescope' && el('start').parentElement===el('picker-action') && document.activeElement===el('device-choice') && !el('selection-help').hidden && el('selection-help').textContent==='Your account has several telescopes. Choose one, then Connect.',{})`

// Scrolling stays inside the repeated probe, including after layout shifts.
const guiTestPickerViewportProbe = `(() => {const top=el('status').getBoundingClientRect().top+scrollY,bottom=el('device-picker').getBoundingClientRect().bottom+scrollY; const fits=bottom-top<=innerHeight; if(fits) window.scrollTo(0,Math.max(0,Math.ceil(bottom-innerHeight))); else el('device-picker').scrollIntoView({block:'center'}); const ids=fits ? ['status','device-choice'] : ['device-choice']; if(!el('custom-device').hidden) ids.push('serial','model','start'); const visible=ids.every(id=>{const r=el(id).getBoundingClientRect();return r.top>=-1 && r.bottom<=innerHeight+1});return check(visible,{ids,innerHeight,rects:ids.map(id=>el(id).getBoundingClientRect())})})()`

const guiTestManualGapProbe = `(() => {
 const action=el('picker-action'),margin=parseFloat(getComputedStyle(action).marginTop),expected=.75*parseFloat(getComputedStyle(document.documentElement).fontSize),gap=el('start').getBoundingClientRect().top-el('custom-device').getBoundingClientRect().bottom;
 return check(!el('custom-device').hidden && Math.abs(margin-expected)<.5 && gap>=expected-.5,{margin,expected,gap});
})()`

// This predicate only observes the natural selection-step viewport.
const guiTestPickerTransitionViewportProbe = `(() => {
 const picker=el('device-choice'),style=getComputedStyle(picker),ring=(parseFloat(style.outlineWidth)||0)+Math.max(0,parseFloat(style.outlineOffset)||0);
 const expand=e=>{const r=e.getBoundingClientRect();return {top:r.top-ring,bottom:r.bottom+ring,left:r.left-ring,right:r.right+ring};};
 const rect=expand(picker),start=expand(el('start')),help=el('selection-help').getBoundingClientRect();
 const group={top:rect.top,bottom:Math.max(help.bottom,start.bottom),left:Math.min(rect.left,start.left),right:Math.max(rect.right,start.right)};
 const fits=group.bottom-group.top<=innerHeight && group.right-group.left<=innerWidth;
 const visible=r=>r.top>=0 && r.bottom<=innerHeight && r.left>=0 && r.right<=innerWidth;
 const focused=document.activeElement===picker;
 return check(focused && !el('device-picker').hidden && visible(rect) && (!fits || (visible(help) && visible(start))),{focused,ring,fits,rect,help,start,innerHeight,innerWidth});
})()`

const guiTestConnectedTelescopeProbe = `check(el('status').textContent==='Connected to Seestar S50 Pro synthetic-choice-1234' && !el('selected-device') && !el('change-telescope') && !el('change-telescope-help') && el('start').textContent==='Stop' && el('start').parentElement===el('primary-action') && el('settings').hidden,{status:el('status').textContent})`

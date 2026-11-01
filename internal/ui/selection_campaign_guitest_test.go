//go:build gui && guitest

package ui

import (
	"os"
	"strings"
	"testing"
)

func TestPickerScreenshotCampaign(t *testing.T) {
	driver, err := os.ReadFile("selection_driver_guitest.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, shot := range []string{
		`d.state("Enter a serial number.", false, "telescope-manual")`,
		`d.state("Stopped. Choose a telescope, then Connect.", false, "telescope-choosing")`,
	} {
		if !strings.Contains(string(driver), shot) {
			t.Fatal("missing picker screenshot", shot)
		}
	}
	viewport, err := os.ReadFile("driver_guitest.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(viewport), `"telescope-manual", "telescope-choosing"`) {
		t.Fatal("new picker screenshots must use the repeated scrolling probe")
	}
}

// Execute the exact native driver predicates against the complete production
// page. Corrupted UI states must be rejected by each new driver assertion.
func TestPickerDriverProbes(t *testing.T) {
	for _, step := range []struct {
		name, setup, probe string
		mutations          map[string]string
	}{
		{"discovery", `render({...idle,devices:[]});`, guiTestDiscoveryPickerProbe, map[string]string{
			"label":       `$('device-choice').children[0].textContent='wrong';`,
			"hidden":      `$('selection-help').hidden=true;`,
			"help":        `$('selection-help').textContent='wrong';`,
			"association": `$('device-choice').setAttribute('aria-describedby','wrong');`,
		}},
		{"manual", `render(multiple); $('device-choice').value='custom'; $('device-choice').onchange();`, guiTestManualPickerProbe, map[string]string{
			"label":       `$('device-choice').children[3].textContent='wrong';`,
			"hidden":      `$('custom-device').hidden=true;`,
			"parent":      `$('options').append($('picker-action'));`,
			"order":       `$('device-picker').append($('custom-device'));`,
			"Connect":     `$('primary-action').append($('start'));`,
			"focus":       `$('start').focus();`,
			"status":      `$('status').textContent='Ready to connect';`,
			"help hidden": `$('serial-help').hidden=true;`,
			"help":        `$('serial-help').textContent='wrong';`,
			"association": `$('serial').setAttribute('aria-describedby','serial-error');`,
		}},
		{"choosing", `render({...idle,choosing:true,status:'Stopped. Choose a telescope, then Connect.'});`, guiTestChoosingPickerProbe, map[string]string{
			"value":       `$('device-choice').value='0';`,
			"label":       `$('device-choice').children[0].textContent='Find on my account';`,
			"status":      `$('status').textContent='Stopped. Choose Connect to pick a telescope.';`,
			"Connect":     `$('primary-action').append($('start'));`,
			"focus":       `$('start').focus();`,
			"help hidden": `$('selection-help').hidden=true;`,
			"help":        `$('selection-help').textContent='wrong';`,
		}},
	} {
		step.mutations["valid"] = ""
		for name, mutation := range step.mutations {
			t.Run(step.name+"/"+name, func(t *testing.T) {
				expected := "false"
				if name == "valid" {
					expected = "true"
				}
				call := `(async () => {const {idle,multiple}=uxFixture(); const el=$,check=ok=>!!ok; ` + step.setup + mutation + `
 if ((` + step.probe + `) !== ` + expected + `) throw new Error('driver predicate accepted a corrupted UI or rejected the valid page');})()`
				runPageRegression(t, "picker-ux-test.js", call)
			})
		}
	}
}

func TestPickerViewportProbe(t *testing.T) {
	script := `
let scrollY=0,innerHeight=600,scrolls=0,manual=true;
const boxes={status:[20,40],'device-picker':[400,900],'device-choice':[440,480],serial:[550,590],model:[650,690],start:[750,790]};
const el=id=>({hidden:id==='custom-device' && !manual,
 getBoundingClientRect(){const [top,bottom]=boxes[id];return {top:top-scrollY,bottom:bottom-scrollY};},
 scrollIntoView(){scrolls++;scrollY=350;}});
const window={scrollTo(x,y){scrolls++;scrollY=y;}};
const check=ok=>!!ok;
const assert=(ok,message)=>{if(!ok)throw new Error(message);};
const probe=()=>` + guiTestPickerViewportProbe + `;
assert(probe() && scrolls===1,'manual viewport did not scroll and match');
scrollY=0;
assert(probe() && scrolls===2,'unchanged viewport probe did not scroll again');
boxes.status=[400,420];
assert(probe(),'fitted status/picker viewport did not match');
for(const id of ['serial','model','start']) {
 const original=boxes[id];
 boxes[id]=[-50,-10];
 assert(!probe(),'viewport accepted clipped '+id);
 boxes[id]=[1200,1240];
 assert(!probe(),'viewport accepted offscreen '+id);
 boxes[id]=original;
}
manual=false;boxes.serial=[-50,-10];
assert(probe(),'hidden manual fields should not block a received-choice capture');
`
	runGUIJavaScript(t, script)
}

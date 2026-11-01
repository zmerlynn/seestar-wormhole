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
	transition := strings.Index(string(viewport), "d.probe(guiTestPickerTransitionViewportProbe)")
	scroll := strings.Index(string(viewport), "\n\tswitch shot {")
	if transition < 0 || scroll < 0 || transition >= scroll {
		t.Fatal("post-Change geometry must be checked before driver scrolling")
	}
	if strings.Contains(guiTestPickerTransitionViewportProbe, "scroll") {
		t.Fatal("post-Change predicate must only observe the viewport")
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

func TestPickerManualGapProbe(t *testing.T) {
	runGUIJavaScript(t, `
let margin=12,gap=12,hidden=false;
const document={documentElement:{}};
const getComputedStyle=e=>({marginTop:margin+'px',fontSize:'16px'});
const el=id=>({hidden,getBoundingClientRect(){return {top:id==='start'?100+gap:0,bottom:100};}});
const check=ok=>!!ok;
const assert=(ok,message)=>{if(!ok)throw new Error(message);};
const probe=()=>`+guiTestManualGapProbe+`;
assert(probe(),'valid manual gap rejected');
margin=0;assert(!probe(),'missing manual margin accepted');
margin=12;gap=0;assert(!probe(),'touching Connect accepted');
gap=12;hidden=true;assert(!probe(),'hidden manual fields accepted');
`)
}

func TestPickerTransitionViewportProbe(t *testing.T) {
	runGUIJavaScript(t, `
const innerHeight=600,innerWidth=800;
let focus=true,hidden=false;
const boxes={'device-choice':{top:400,bottom:444,left:20,right:740},'selection-help':{top:450,bottom:480,left:20,right:740},start:{top:500,bottom:544,left:20,right:140}};
const elements=Object.fromEntries(Object.keys(boxes).concat(['device-picker']).map(id=>[id,{get hidden(){return hidden;},getBoundingClientRect(){return {...boxes[id]};}}]));
const el=id=>elements[id];
const document={get activeElement(){return focus?el('device-choice'):el('start');}};
const getComputedStyle=()=>({outlineWidth:'3px',outlineOffset:'3px'});
const check=ok=>!!ok;
const assert=(ok,message)=>{if(!ok)throw new Error(message);};
const probe=()=>`+guiTestPickerTransitionViewportProbe+`;
assert(probe(),'valid natural viewport rejected');
for(const id of ['device-choice','start']) {
 for(const [axis,value] of [['top',2],['bottom',598],['left',2],['right',798]]) {
  const original={...boxes[id]};
  // Move the whole control, preserving its size and a group that still fits.
  const other={top:'bottom',bottom:'top',left:'right',right:'left'}[axis];
  const delta=value-boxes[id][axis];boxes[id][axis]+=delta;boxes[id][other]+=delta;
  assert(!probe(),'clipped focus outline accepted: '+id+' '+axis);
  boxes[id]=original;
 }
}
const help={...boxes['selection-help']};boxes['selection-help'].left=-1;
assert(!probe(),'offscreen help accepted for fitted group');boxes['selection-help']=help;
focus=false;assert(!probe(),'unfocused picker accepted');focus=true;
hidden=true;assert(!probe(),'hidden picker accepted');hidden=false;
boxes.start.top=1100;boxes.start.bottom=1144;
assert(probe(),'oversized group must only require picker outline');
boxes['device-choice'].top=2;boxes['device-choice'].bottom=46;
assert(!probe(),'oversized group accepted clipped picker outline');
`)
}

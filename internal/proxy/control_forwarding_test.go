package proxy

import (
	"bytes"
	"strings"
	"testing"
)

// Literal event shapes with synthetic values. The proxy
// forwards device bytes without imposing a client schema, state enum, event
// ordering or authentication requirement on delivery.
func TestControlEventForwarding(t *testing.T) {
	for _, tc := range []struct{ name, line string }{
		{"plan-cancel", `{"Event":"ViewPlan","Timestamp":"1.000000001","state":"cancel","lapse_ms":12,"plan":{"list":[]}}`},
		{"exposure-start", `{"Event":"Exposure","Timestamp":"1.000000002","page":"Stack","state":"start","exp_us":1000000,"gain":80}`},
		{"exposure-downloading", `{"Event":"Exposure","Timestamp":"1.000000003","page":"Stack","state":"downloading"}`},
		{"exposure-fail", `{"Event":"Exposure","Timestamp":"1.000000004","page":"Stack","state":"fail","error":"synthetic failure","code":514}`},
		{"exposure-working", `{"Event":"Exposure","Timestamp":"1.000000005","state":"working","lapse_ms":2,"exp_ms":1000.0,"port":4700,"route":["View","Exposure"]}`},
		{"stack-frame-complete", `{"Event":"Stack","Timestamp":"1.000000006","state":"frame_complete","frame_type":"light","stacked_frame":1,"dropped_frame":0,"total_frame":1,"error":"","lapse_ms":5,"code":0}`},
		{"device-temperature", `{"Event":"PiStatus","Timestamp":"1.000000007","temp":20.5}`},
		{"battery-temperature", `{"Event":"PiStatus","Timestamp":"1.000000008","battery_temp":25}`},
		{"disk-space", `{"Event":"DiskSpace","Timestamp":"1.000000009","used_percent":42}`},
		{"version-event", `{"Event":"Version","Timestamp":"1.000000010","name":"synthetic"}`},
		{"rtsp-working", `{"Event":"RTSP","Timestamp":"1.000000011","state":"working","port":4554,"roi_index":0,"roi_scale":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := tc.line + "\r\n" + `{"id":7,"code":0,"result":0}` + "\r\n"
			for _, chunk := range []int{1, 7, 8196} {
				var out bytes.Buffer
				w := &siteClientWriter{writer: &out}
				if err := w.copyDevice(&siteChunkReader{b: []byte(wire), chunk: chunk}); err != nil {
					t.Fatal(err)
				}
				if out.String() != wire {
					t.Fatalf("event or following reply changed at chunk %d", chunk)
				}
			}
		})
	}
}

// The bridge neither generates initialization
// nor inserts verification. The device determines verification enforcement.
func TestControlHandshakeForwarding(t *testing.T) {
	const wire = "{\"id\":1,\"method\":\"pi_get_time\"}\r\n{\"id\":2,\"method\":\"get_verify_str\"}\r\n{\"id\":3,\"method\":\"verify_client\",\"params\":{}}\r\n"
	var out bytes.Buffer
	if err := copySite(&out, strings.NewReader(wire), nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if out.String() != wire {
		t.Fatal("bridge changed verification order or added initialization")
	}
}

// Observed Android 3.3.2 behavior: the app
// sends these writes/reads on each new control socket, including reconnects.
func TestControlAndroidSiteProtection(t *testing.T) {
	const clock = `{"id":1,"method":"pi_set_time","params":[{"year":2000,"mon":1,"day":1,"hour":1,"min":1,"sec":1,"time_zone":"Etc/GMT+8","keep":true}]}`
	const rest = "{\"id\":2,\"method\":\"set_setting\",\"params\":{\"lang\":\"en\"}}\r\n" +
		"{\"id\":3,\"method\":\"set_setting\",\"params\":{\"cli_name\":\"synthetic phone\",\"cli_uuid\":\"synthetic-client\"}}\r\n" +
		"{\"id\":4,\"method\":\"get_device_state\",\"params\":{\"keys\":[\"client\",\"camera\",\"second_camera\"]}}\r\n" +
		"{\"id\":5,\"method\":\"get_setting\"}\r\n" +
		"{\"id\":6,\"method\":\"iscope_get_app_state\"}\r\n" +
		"{\"id\":7,\"method\":\"get_device_state\",\"params\":{\"keys\":[\"device\"]}}\r\n" +
		"{\"id\":8,\"method\":\"get_device_state\"}\r\n"
	for socket := 0; socket < 2; socket++ {
		for _, chunk := range []int{1, 8196} {
			var out, replies bytes.Buffer
			if err := copySite(&out, &siteChunkReader{b: []byte(clock + "\r\n" + rest), chunk: chunk}, nil, &replies); err != nil {
				t.Fatal(err)
			}
			const want = `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"site_protected"}}` + "\r\n"
			if out.String() != rest || replies.String() != want {
				t.Fatalf("socket %d chunk %d: startup=%q replies=%s", socket, chunk, out.String(), replies.String())
			}

		}
	}
}

package p2ptunnel

import (
	"bytes"
	"reflect"
	"testing"
)

// LOGIN, verification and SETUP literals are synthetic field examples, not a
// device capture.
func TestLiteralCatalogMeaning(t *testing.T) {
	for _, tt := range []struct {
		name, wire, payload string
		cmd                 Command
		ch                  byte
		phase               Phase
		body                any
	}{
		{"setup-legacy", "010710000200125c7f0000010000000000000000", "0200125c7f0000010000000000000000", SetupCommand, 7, Established, Setup{Port: 4700, Address: [4]byte{127, 0, 0, 1}, Legacy: true}},
		{"setup", "010734000200125c7f0000010000000000000000780000000000000000000000000000000000000000000000000000000000000000000000", "0200125c7f0000010000000000000000780000000000000000000000000000000000000000000000000000000000000000000000", SetupCommand, 7, Established, Setup{Port: 4700, Address: [4]byte{127, 0, 0, 1}, Service: "x"}},
		{"data", "02070300616263", "616263", DataCommand, 7, Established, nil},
		{"end", "03070000", "", EndCommand, 7, Established, nil},
		{"close", "0407010002", "02", CloseCommand, 7, Established, nil},
		{"close-empty", "04070000", "", CloseCommand, 7, Established, nil},
		{"pause", "05070000", "", PauseCommand, 7, Established, nil},
		{"resume", "06070000", "", ResumeCommand, 7, Established, nil},
		{"wake", "07070000", "", WakeCommand, 7, Established, nil},
		{"login", "070008000008030400000000", "0008030400000000", LoginCommand, 0, LoginRequest, Login{Version: AgentVersion, Auth: []byte{}}},
		{"login-empty", "07000000", "", LoginCommand, 0, LoginRequest, Login{Empty: true}},
		{"pass", "080008001100000001020304", "1100000001020304", PassCommand, 0, LoginResponse, LoginResult{17, 1, 2, 3, 4}},
		{"reject", "09000800efffffff01020304", "efffffff01020304", RejectCommand, 0, LoginResponse, LoginResult{-17, 1, 2, 3, 4}},
		{"port-request", "0c00060034125c120000", "34125c120000", PortRequest, 0, Established, PortCheck{ID: 0x1234, Port: 4700}},
		{"port-response", "0d00060034125c120100", "34125c120100", PortResponse, 0, Established, PortCheck{0x1234, 4700, 1, 0}},
		{"service-request", "0e002400010078000000000000000000000000000000000000000000000000000000000000000000", "010078000000000000000000000000000000000000000000000000000000000000000000", ServiceRequest, 0, Established, ServiceCheck{1, "x", 0}},
		{"service-response", "0f002400010078000000000000000000000000000000000000000000000000000000000000000001", "010078000000000000000000000000000000000000000000000000000000000000000001", ServiceResponse, 0, Established, ServiceCheck{1, "x", 1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wire := unhex(t, tt.wire)
			payload := unhex(t, tt.payload)
			got, err := Decode(wire, tt.phase)
			if err != nil || got.Command != tt.cmd || got.Channel != tt.ch || !bytes.Equal(got.Payload, payload) {
				t.Fatalf("decode=%+v, %v; want command=%d channel=%d payload=%x", got, err, tt.cmd, tt.ch, payload)
			}
			out, err := (Frame{tt.cmd, tt.ch, payload}).Encode(tt.phase)
			if err != nil || !bytes.Equal(out, wire) {
				t.Fatalf("encode=%x, %v; want %x", out, err, wire)
			}
			var body any
			var encoded []byte
			switch want := tt.body.(type) {
			case Setup:
				body, err = ParseSetup(payload)
				encoded, _ = want.Encode()
			case Login:
				body, err = ParseLogin(payload)
				encoded, _ = want.Encode()
			case LoginResult:
				body, err = ParseLoginResult(payload)
				encoded = want.Encode()
			case PortCheck:
				body, err = ParsePort(payload)
				encoded = want.Encode()
			case ServiceCheck:
				body, err = ParseService(payload)
				encoded, _ = want.Encode()
			default:
				return
			}
			if err != nil || !reflect.DeepEqual(body, tt.body) || !bytes.Equal(encoded, payload) {
				t.Fatalf("body=%+v, %v encode=%x; want %+v / %x", body, err, encoded, tt.body, payload)
			}
		})
	}
}

func TestLiteralLoginResultSignPolicy(t *testing.T) {
	for _, tt := range []struct {
		wire   string
		status Status
	}{
		{"080008000000000000000000", Active},
		{"080008001100000000000000", Active},
		{"08000800efffffff00000000", Active},
		{"090008001100000000000000", Stopped},
	} {
		d := newDriver(t, DefaultConfig())
		d.step(Event{Kind: Start})
		a := d.step(Event{Kind: Stream, Data: unhex(t, tt.wire)})
		_, ready := find(a, SessionReady)
		if d.m.Status() != tt.status || ready != (tt.status == Active) {
			t.Fatalf("%s: status=%v ready=%v; want %v", tt.wire, d.m.Status(), ready, tt.status)
		}
	}
}

func TestLiteralFlowControlTransitions(t *testing.T) {
	d := activeDriver(t, DefaultConfig(), false)
	ch := d.open(4700)
	for _, tt := range []struct {
		wire  string
		state byte
	}{
		{"05010000", 2}, {"05010000", 4}, {"06010000", 0}, {"07010000", 2},
	} {
		d.step(Event{Kind: Stream, Data: unhex(t, tt.wire)})
		if got := d.m.Channel(ch.Channel).ReadState; got != tt.state {
			t.Fatalf("%s state=%d; want %d", tt.wire, got, tt.state)
		}
	}
	d.step(Event{Kind: Stream, Data: unhex(t, "03010000")})
	if !d.m.Channel(ch.Channel).WriteEnd {
		t.Fatal("literal END did not half-close")
	}
	d.step(Event{Kind: Stream, Data: unhex(t, "0401010001")})
	if d.m.Channel(ch.Channel).Active {
		t.Fatal("literal CLOSE did not release")
	}
}

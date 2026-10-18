package dtls

import (
	"bytes"
	"testing"

	"github.com/pion/dtls/v3/pkg/protocol"
	"github.com/pion/dtls/v3/pkg/protocol/recordlayer"
)

func TestE2ERecordDecoderAgreement(t *testing.T) {
	// Independent RFC 6347 envelopes. Admission adds size and plaintext-app
	// policy, but framing and version acceptance must agree with Pion.
	headerOnly := []byte{22, 0xfe, 0xfd, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	control := []byte{20, 0xfe, 0xfd, 0, 0, 0, 0, 0, 0, 0, 1, 0, 1, 1}
	app := []byte{23, 0xfe, 0xfd, 0, 1, 0, 0, 0, 0, 0, 2, 0, 1, 42}
	unknown := bytes.Clone(control)
	unknown[0] = 99
	oldVersion := bytes.Clone(control)
	oldVersion[2] = 0xff
	badVersion := bytes.Clone(control)
	badVersion[2] = 0xfc
	oversizedLength := bytes.Clone(control)
	oversizedLength[12] = 2
	for _, tc := range []struct {
		name string
		wire []byte
	}{
		{"header_only", headerOnly},
		{"control", control},
		{"application", app},
		{"unknown_type", unknown},
		{"dtls_1_0", oldVersion},
		{"bad_version", badVersion},
		{"truncated_body", oversizedLength},
		{"truncated_header", control[:12]},
		{"coalesced", bytes.Join([][]byte{app, control, unknown, app}, nil)},
		{"header_only_prefix", bytes.Join([][]byte{headerOnly, control}, nil)},
		{"header_only_suffix", bytes.Join([][]byte{control, headerOnly}, nil)},
		{"bad_header_suffix", bytes.Join([][]byte{control, badVersion}, nil)},
		{"trailing_byte", append(bytes.Clone(control), 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			records, libraryErr := recordlayer.UnpackDatagram(tc.wire)
			want := 0
			if libraryErr == nil {
				for _, record := range records {
					var h recordlayer.Header
					if libraryErr = h.Unmarshal(record); libraryErr != nil {
						break
					}
					if h.ContentType != protocol.ContentTypeApplicationData {
						want += len(record)
					}
				}
			}
			got, admissionErr := ControlBytes(tc.wire)
			if (admissionErr == nil) != (libraryErr == nil) {
				t.Fatalf("DTLS decoders disagree: admission=%v, Pion=%v", admissionErr, libraryErr)
			}
			if admissionErr == nil && got != want {
				t.Fatalf("control bytes = %d, want Pion record sum %d", got, want)
			}
		})
	}
}

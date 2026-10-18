package dtls

import (
	"bytes"
	"crypto/sha256"
	"errors"

	"github.com/pion/dtls/v3/pkg/protocol"
	"github.com/pion/dtls/v3/pkg/protocol/recordlayer"
)

// Profile contains only public negotiated parameters, safe for readiness logs.
type Profile struct{ Version, Suite uint16 }

// Conditional profile constants describe a permitted offer, not a captured
// device negotiation.
const (
	Version12    uint16 = 0xfefd
	CCAC         uint16 = 0xccac
	PSKAES128GCM uint16 = 0x00a8
	// MaxDatagram is the native secure BIO receive size.
	MaxDatagram = 1388
	// MaxApplication is one complete normal RDT packet (20 + 1280 bytes).
	MaxApplication = 1300
)

// Valid reports whether the authenticated negotiation matches the allowed offer.
// This allowed subset is local policy under the conditional compiled-default profile:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/dtls.md#version-and-cipher-suite
func (p Profile) Valid() bool {
	return p.Version == Version12 && (p.Suite == CCAC || p.Suite == PSKAES128GCM)
}

// Credentials applies the native C-string boundary to already encoded JNI bytes.
// Callers supply bytes, not Go strings: this function does not invent a JNI encoding.
// The 255-byte identity leaves a NUL slot in the native 256-byte callback;
// the 32-byte password limit conservatively matches the firmware callback.
// Returned identity is owned; neither input is retained. Never log these values.
// Identity bytes and raw SHA-256 password PSK:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/dtls.md#identity-and-psk
func Credentials(username, password []byte) ([]byte, [32]byte, error) {
	if i := bytes.IndexByte(username, 0); i >= 0 {
		username = username[:i]
	}
	if i := bytes.IndexByte(password, 0); i >= 0 {
		password = password[:i]
	}
	if len(username) == 0 || len(username) > 255 || len(password) == 0 || len(password) > 32 {
		return nil, [32]byte{}, errors.New("dtls: invalid credential length")
	}
	return bytes.Clone(username), sha256.Sum256(password), nil
}

// ControlBytes returns the control byte count from DecodeDatagram.
func ControlBytes(p []byte) (int, error) {
	_, n, err := DecodeDatagram(p)
	return n, err
}

// DecodeDatagram uses Pion to split records and decode their public headers.
// It returns control record slices in wire order and their total byte count,
// borrowing p until the caller copies them. Payloads remain opaque, including
// encrypted handshake records; no record is authenticated here.
// Local policy bounds the datagram and rejects epoch-zero application data.
// Record headers are not authentication; local admission bounds supplement encapsulation:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/dtls.md#encapsulation-and-role
func DecodeDatagram(p []byte) ([][]byte, int, error) {
	if len(p) == 0 || len(p) > MaxDatagram {
		return nil, 0, errors.New("dtls: invalid datagram size")
	}
	records, err := recordlayer.UnpackDatagram(p)
	if err != nil {
		return nil, 0, err
	}
	control := records[:0]
	n := 0
	for _, record := range records {
		var h recordlayer.Header
		if err := h.Unmarshal(record); err != nil {
			return nil, 0, err
		}
		if h.ContentType == protocol.ContentTypeApplicationData {
			if h.Epoch == 0 {
				return nil, 0, errors.New("dtls: epoch-zero application data")
			}
			continue
		}
		control = append(control, record)
		n += len(record)
	}
	return control, n, nil
}

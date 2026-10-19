// Package wiretest protects synthetic receive fixtures with Pion's DTLS record
// codec and cipher. It is used only by tests; it does not simulate negotiation.
//
// Wire reference: https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/dtls.md
package wiretest

import (
	"bytes"
	"errors"
	"testing"

	cs "github.com/pion/dtls/v3/pkg/crypto/ciphersuite"
	"github.com/pion/dtls/v3/pkg/protocol"
	"github.com/pion/dtls/v3/pkg/protocol/recordlayer"
)

var errRecord = errors.New("invalid synthetic DTLS record")

// Key returns public synthetic test material, never production credentials.
func Key() []byte { return bytes.Repeat([]byte{0x42}, 32) }

// IV returns the public synthetic write IV.
func IV() []byte { return bytes.Repeat([]byte{0x24}, 12) }

// Seal uses an epoch-one application record and the supplied sequence number.
func Seal(t testing.TB, plain []byte, sequence uint64) []byte {
	t.Helper()
	c, err := cs.NewChaCha20Poly1305(Key(), IV(), Key(), IV())
	if err != nil {
		t.Fatal(err)
	}
	r := recordlayer.RecordLayer{Header: recordlayer.Header{Version: protocol.Version1_2, Epoch: 1, SequenceNumber: sequence}, Content: &protocol.ApplicationData{Data: plain}}
	raw, err := r.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	raw, err = c.Encrypt(&r, raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Open authenticates a complete record using maintained DTLS framing/crypto.
func Open(wire, key []byte) ([]byte, error) {
	records, err := recordlayer.UnpackDatagram(wire)
	if err != nil {
		return nil, err
	}
	if len(records) != 1 {
		return nil, errRecord
	}
	var h recordlayer.Header
	if err = h.Unmarshal(records[0]); err != nil {
		return nil, err
	}
	if h.ContentType != protocol.ContentTypeApplicationData || h.Epoch != 1 {
		return nil, errRecord
	}
	c, err := cs.NewChaCha20Poly1305(key, IV(), key, IV())
	if err != nil {
		return nil, err
	}
	raw, err := c.Decrypt(h, bytes.Clone(records[0]))
	if err != nil {
		return nil, err
	}
	var r recordlayer.RecordLayer
	if err = r.Unmarshal(raw); err != nil {
		return nil, err
	}
	return r.Content.(*protocol.ApplicationData).Data, nil
}

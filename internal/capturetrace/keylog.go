package capturetrace

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"io"
	"strconv"
	"strings"
)

// This is the repository's own vendor extension, not an NSS/DTLS parser.
// NSS lines and unrelated comments are ignored. The selected attempt/ref must
// identify one unambiguous pair; identical repeated entries are harmless.
func readKey(in io.Reader, limit int, attempt uint64) (*keyPair, error) {
	data, err := bounded(in, limit, "keylog bytes")
	if err != nil {
		return nil, err
	}
	var selected *keyPair
	var selectedKey, selectedIV, selectedRID []byte
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, min(limit, 4096)), limit+1)
	bad := func() error { return &DecodeError{Kind: MalformedKeylog} }
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[0] != "#" || fields[1] != "IOTC_KEY" {
			continue
		}
		if len(fields) != 7 {
			return nil, bad()
		}
		a, e1 := strconv.ParseUint(fields[2], 10, 64)
		ref, e2 := strconv.ParseUint(fields[4], 10, 64)
		rid, e3 := hex.DecodeString(fields[3])
		key, e4 := hex.DecodeString(fields[5])
		iv, e5 := hex.DecodeString(fields[6])
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || len(rid) != 8 || len(key) != 16 || len(iv) != 12 {
			return nil, bad()
		}
		if a != attempt || ref != 3 {
			continue
		}
		if selected != nil {
			if !bytes.Equal(key, selectedKey) || !bytes.Equal(iv, selectedIV) || !bytes.Equal(rid, selectedRID) {
				return nil, bad()
			}
			continue
		}
		selected, err = makePair(key, iv)
		if err != nil {
			return nil, bad()
		}
		selectedKey, selectedIV, selectedRID = key, iv, rid
	}
	if scanner.Err() != nil {
		return nil, bad()
	}
	return selected, nil
}

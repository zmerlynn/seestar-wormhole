package keylog

import "fmt"

// Rendezvous enqueues an attempt's AES-128-GCM pair before use. RID identifies
// the attempt on wire;
// ref is local, with 3 denoting master material and later refs server pairs.
// The comment prefix lets NSS readers ignore this private extension.
// The log extension records the attempt-scoped pair whose lifetime is specified here:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/rendezvous-gcm.md#gcm-key-iv-and-cache-contract
func (w *Writer) Rendezvous(attempt uint64, rid [8]byte, ref uint64, key []byte, iv [12]byte) error {
	if w == nil {
		return nil
	}
	if len(key) != 16 {
		w.drop()
		return nil
	}
	_, err := fmt.Fprintf(w, "# IOTC_KEY %d %x %d %x %x\n", attempt, rid, ref, key, iv)
	return err
}

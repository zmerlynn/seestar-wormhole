package ndr

import (
	"fmt"
	"reflect"
)

// writePipe writes a slice as an NDR pipe. The decoded representation is a flat
// slice with the original chunk boundaries lost, so the encoder emits a single
// chunk containing all elements followed by a terminating zero-length chunk.
func (enc *Encoder) writePipe(v reflect.Value, tag reflect.StructTag) error {
	n := v.Len()
	if err := enc.writeUint32(uint32(n)); err != nil { // element count of the chunk
		return err
	}
	for i := 0; i < n; i++ {
		if err := enc.fill(v.Index(i), tag, &[]deferedPtr{}); err != nil {
			return fmt.Errorf("could not write element %d of pipe: %v", i, err)
		}
	}
	// terminating zero-length chunk
	return enc.writeUint32(0)
}

package ndr

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
)

// writeRawBytes writes a fixed number of bytes for a type implementing the
// RawBytes interface. The size is taken from the tag, populated by addSizeToTag.
func (enc *Encoder) writeRawBytes(v reflect.Value, tag reflect.StructTag) error {
	ndrTag := parseTags(tag)
	sizeStr, ok := ndrTag.Map["size"]
	if !ok {
		return errors.New("size tag not available")
	}
	size, err := strconv.Atoi(sizeStr)
	if err != nil {
		return fmt.Errorf("size not valid: %v", err)
	}
	b := v.Bytes()
	if len(b) < size {
		return fmt.Errorf("raw bytes length %d is less than size %d", len(b), size)
	}
	return enc.writeBytes(b[:size])
}

package ndr

import (
	"fmt"
	"reflect"
)

// isUnion establishes whether a field is a union discriminant tag. If it is and
// the union is non-encapsulated, the discriminant is marshalled into the stream
// an extra time before the union representation (mirroring the decoder, which
// discards that copy). The returned value is the discriminant field.
func (enc *Encoder) isUnion(field reflect.Value, tag reflect.StructTag) (r reflect.Value, err error) {
	ndrTag := parseTags(tag)
	if !ndrTag.HasValue(TagUnionTag) {
		return
	}
	r = field
	// For a non-encapsulated union, the discriminant is marshalled into the
	// transmitted data stream twice: once here, before the union, and once as
	// the discriminant field itself.
	if !ndrTag.HasValue(TagEncapsulated) {
		if err = enc.fill(r, reflect.StructTag(""), &[]deferedPtr{}); err != nil {
			return r, fmt.Errorf("could not write union discriminant: %v", err)
		}
	}
	return
}

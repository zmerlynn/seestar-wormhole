package ndr

import (
	"fmt"
	"reflect"
)

// stringToUint16Slice is the inverse of uint16SliceToString: each rune is cast
// to a uint16 and a null terminator is appended. NDR strings are conventionally
// null-terminated and the decoder strips a single trailing null, so the
// terminator is added back here. Runes outside the BMP are truncated to a
// single uint16, mirroring the decoder's rune(uint16) handling.
func stringToUint16Slice(s string) []uint16 {
	r := []rune(s)
	u := make([]uint16, len(r)+1)
	for i, c := range r {
		u[i] = uint16(c)
	}
	// final element is the zero (null) terminator
	return u
}

// stringUTF16Len returns the number of uint16 code units used to encode s,
// including the null terminator.
func stringUTF16Len(s string) int {
	return len([]rune(s)) + 1
}

// maxStringUTF16Len returns the largest stringUTF16Len of any string element
// nested within v, used as the common conformance max for string arrays.
func maxStringUTF16Len(v reflect.Value) int {
	switch v.Kind() {
	case reflect.String:
		return stringUTF16Len(v.String())
	case reflect.Slice, reflect.Array:
		m := 0
		for i := 0; i < v.Len(); i++ {
			if n := maxStringUTF16Len(v.Index(i)); n > m {
				m = n
			}
		}
		return m
	default:
		return 0
	}
}

// writeVaryingString writes a string as an NDR varying array of uint16.
func (enc *Encoder) writeVaryingString(s string) error {
	u := stringToUint16Slice(s)
	a := reflect.ValueOf(u)
	var t reflect.StructTag
	if err := enc.writeUniDimensionalVaryingArray(a, t, &[]deferedPtr{}); err != nil {
		return err
	}
	return nil
}

// writeConformantVaryingString writes a string as an NDR conformant varying
// array of uint16. The max count was hoisted to the front of the structure.
func (enc *Encoder) writeConformantVaryingString(s string) error {
	u := stringToUint16Slice(s)
	a := reflect.ValueOf(u)
	var t reflect.StructTag
	if err := enc.writeUniDimensionalConformantVaryingArray(a, t, &[]deferedPtr{}); err != nil {
		return err
	}
	return nil
}

// writeStringsArray writes an array of strings, mirroring readStringsArray.
func (enc *Encoder) writeStringsArray(v reflect.Value, tag reflect.StructTag, def *[]deferedPtr) error {
	d, _ := sliceDimensions(v.Type())
	ndrTag := parseTags(tag)
	if ndrTag.HasValue(TagConformant) {
		// The per-dimension max counts and the common string max were hoisted
		// to the front of the structure; consume them.
		for i := 0; i < d; i++ {
			_ = enc.precedingMax()
		}
		_ = enc.precedingMax()
	}
	tag = reflect.StructTag(subStringArrayTag)
	if err := enc.writeVaryingArray(v, tag, def); err != nil {
		return fmt.Errorf("could not write string array: %v", err)
	}
	return nil
}

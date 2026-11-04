package ndr

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"reflect"
)

// Referent identifiers used when marshaling NDR pointers. The decoder only
// cares whether a referent id is zero (nil pointer) or non-zero (present); the
// actual value is implementation defined for "unique" pointers, which is the
// pointer flavour used by Type Serialization v1. These fixed values mirror the
// ones used in the decoder's own test vectors so that decode->encode produces
// byte-identical output.
const (
	// topLevelReferent is the RPC unique pointer referent that prefixes the
	// top-level type. The decoder skips these 4 bytes in Decode.
	topLevelReferent uint32 = 0x00020000
	// deferredReferent is written inline for every non-nil deferred pointer.
	deferredReferent uint32 = 0x02000400
)

// Encoder marshals a Go struct representation into an NDR byte stream. It is the
// inverse of Decoder: it honours the same ndr struct tags, alignment rules,
// conformant-array max-count hoisting, deferred pointers and the Type
// Serialization v1 common/private headers.
type Encoder struct {
	w             io.Writer     // destination of the encoded data
	buf           *bytes.Buffer // assembles the whole stream so alignment is relative to byte 0
	ch            CommonHeader  // NDR common header (provides endianness)
	conformantMax []uint32      // conformant max values hoisted to the beginning of the structure
}

// NewEncoder creates a new instance of an NDR Encoder.
func NewEncoder(w io.Writer) *Encoder {
	enc := new(Encoder)
	enc.w = w
	enc.buf = new(bytes.Buffer)
	enc.ch.Endianness = binary.LittleEndian
	return enc
}

// Marshal returns the NDR encoding of s.
func Marshal(s interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Encode marshals the pointer of a struct provided into NDR encoded bytes.
func (enc *Encoder) Encode(s interface{}) error {
	if err := enc.writeCommonHeader(); err != nil {
		return err
	}
	// Reserve the private header. The object buffer length is not known until
	// the body has been written so a placeholder is emitted and backfilled.
	if err := enc.writePrivateHeader(0); err != nil {
		return err
	}
	// The next 4 bytes are the RPC unique pointer referent for the top-level
	// type. The decoder skips these; we emit the same value the decoder's
	// vectors use.
	if err := enc.writeUint32(topLevelReferent); err != nil {
		return Errorf("unable to write top-level referent: %v", err)
	}

	if err := enc.process(s, reflect.StructTag("")); err != nil {
		return err
	}

	// The object buffer length excludes the header itself and must include the
	// terminal padding that aligns the buffer to a multiple of 8. Emit that
	// padding so the stream actually contains objLen body bytes, then backfill
	// the length into the private header.
	bodyLen := enc.buf.Len() - int(commonHeaderBytes) - 8
	objLen := roundUpToMultiple(bodyLen, 8)
	if pad := objLen - bodyLen; pad > 0 {
		enc.buf.Write(make([]byte, pad))
	}
	out := enc.buf.Bytes()
	enc.ch.Endianness.PutUint32(out[commonHeaderBytes:commonHeaderBytes+4], uint32(objLen))

	_, err := enc.w.Write(out)
	return err
}

// roundUpToMultiple rounds n up to the next multiple of m.
func roundUpToMultiple(n, m int) int {
	if r := n % m; r != 0 {
		return n + (m - r)
	}
	return n
}

func (enc *Encoder) process(s interface{}, tag reflect.StructTag) error {
	// Emit the conformant max counts that NDR hoists to the beginning of the
	// structure. http://pubs.opengroup.org/onlinepubs/9629399/chap14.htm#tagfcjh_37
	if err := enc.scanConformantArrays(s, tag); err != nil {
		return err
	}
	// Recursively write the struct fields, collecting any deferred referents.
	var localDef []deferedPtr
	if err := enc.fill(s, tag, &localDef); err != nil {
		return Errorf("could not encode: %v", err)
	}
	// Write any deferred referents associated with pointers, in referent order.
	for _, p := range localDef {
		if err := enc.process(p.v, p.tag); err != nil {
			return fmt.Errorf("could not encode deferred referent: %v", err)
		}
	}
	return nil
}

// scanConformantArrays scans the structure for embedded conformant fields and
// writes the maximum element counts that NDR moves to the beginning of the
// structure.
func (enc *Encoder) scanConformantArrays(s interface{}, tag reflect.StructTag) error {
	if err := enc.conformantScan(s, tag); err != nil {
		return fmt.Errorf("failed to scan for embedded conformant arrays: %v", err)
	}
	for i := range enc.conformantMax {
		if err := enc.writeUint32(enc.conformantMax[i]); err != nil {
			return fmt.Errorf("could not write preceding conformant max count index %d: %v", i, err)
		}
	}
	return nil
}

// conformantScan inspects the structure's fields for whether they are
// conformant and captures the max element counts derived from the data.
func (enc *Encoder) conformantScan(s interface{}, tag reflect.StructTag) error {
	ndrTag := parseTags(tag)
	if ndrTag.HasValue(TagPointer) {
		return nil
	}
	v := getReflectValue(s)
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if err := enc.conformantScan(v.Field(i), v.Type().Field(i).Tag); err != nil {
				return err
			}
		}
	case reflect.String:
		if !ndrTag.HasValue(TagConformant) {
			break
		}
		enc.conformantMax = append(enc.conformantMax, uint32(stringUTF16Len(v.String())))
	case reflect.Slice:
		if !ndrTag.HasValue(TagConformant) {
			break
		}
		d, t := sliceDimensions(v.Type())
		dims := sliceDimLengths(v, d)
		for i := 0; i < d; i++ {
			enc.conformantMax = append(enc.conformantMax, uint32(dims[i]))
		}
		// For string arrays there is a common max for the strings within the array.
		if t.Kind() == reflect.String {
			enc.conformantMax = append(enc.conformantMax, uint32(maxStringUTF16Len(v)))
		}
	}
	return nil
}

// isPointer establishes whether a field is an NDR pointer. If it is, a 4-byte
// referent id is written inline and, when present, the referent is queued for
// deferred writing after the current structure.
//
// A pointer-tagged field equal to its type's zero value is treated as a NULL
// pointer (zero referent id). This covers nil pointers, slices and maps as well
// as zero-value value types (e.g. an all-zero struct tagged as an NDR pointer,
// such as an absent RPC_SID). It mirrors the decoder, which represents a NULL
// pointer by leaving the field at its zero value: encoding the zero value as
// NULL therefore round-trips. A present, fully-zero value type is invalid in
// practice (e.g. an RPC_SID has Revision 1) and is indistinguishable from NULL
// to the decoder, so this loses no recoverable information.
func (enc *Encoder) isPointer(v reflect.Value, tag reflect.StructTag, def *[]deferedPtr) (bool, error) {
	ndrTag := parseTags(tag)
	if ndrTag.HasValue(TagPointer) {
		ndrTag.delete(TagPointer)
		if v.IsZero() {
			if err := enc.writeUint32(0); err != nil {
				return true, fmt.Errorf("could not write nil pointer: %v", err)
			}
			return true, nil
		}
		if err := enc.writeUint32(deferredReferent); err != nil {
			return true, fmt.Errorf("could not write pointer referent: %v", err)
		}
		// Add the referent to the deferred items written at the end of the structure.
		*def = append(*def, deferedPtr{v, ndrTag.StructTag()})
		return true, nil
	}
	return false, nil
}

// fill writes the values of fields to the NDR byte stream.
func (enc *Encoder) fill(s interface{}, tag reflect.StructTag, localDef *[]deferedPtr) error {
	v := getReflectValue(s)

	// Pointer so defer writing the referent.
	ptr, err := enc.isPointer(v, tag, localDef)
	if err != nil {
		return fmt.Errorf("could not process pointer field: %v", err)
	}
	if ptr {
		return nil
	}

	switch v.Kind() {
	case reflect.Struct:
		// In case the struct is a union, track the discriminant and the selected field.
		var unionTag reflect.Value
		var unionField string
		for i := 0; i < v.NumField(); i++ {
			fieldName := v.Type().Field(i).Name
			structTag := v.Type().Field(i).Tag
			ndrTag := parseTags(structTag)

			// Union handling
			if !unionTag.IsValid() {
				// Is this field the union discriminant tag?
				unionTag, err = enc.isUnion(v.Field(i), structTag)
				if err != nil {
					return err
				}
			} else {
				// What is the selected field value of the union if we don't already know.
				if unionField == "" {
					unionField, err = unionSelectedField(v, unionTag)
					if err != nil {
						return fmt.Errorf("could not determine selected union value field for %s with discriminant"+
							" tag %s: %v", v.Type().Name(), unionTag, err)
					}
				}
				if ndrTag.HasValue(TagUnionField) && fieldName != unionField {
					// Is a union and this field has not been selected so will skip it.
					continue
				}
			}

			if v.Field(i).Type().Implements(reflect.TypeOf(new(RawBytes)).Elem()) &&
				v.Field(i).Type().Kind() == reflect.Slice && v.Field(i).Type().Elem().Kind() == reflect.Uint8 {
				// field is for rawbytes
				structTag, err = addSizeToTag(v, v.Field(i), structTag)
				if err != nil {
					return fmt.Errorf("could not get rawbytes field size: %v", err)
				}
				ptr, err := enc.isPointer(v.Field(i), structTag, localDef)
				if err != nil {
					return fmt.Errorf("could not process struct field: %v", err)
				}
				if !ptr {
					if err := enc.writeRawBytes(v.Field(i), structTag); err != nil {
						return fmt.Errorf("could not write raw bytes struct field: %v", err)
					}
				}
			} else {
				if err := enc.fill(v.Field(i), structTag, localDef); err != nil {
					return fmt.Errorf("could not write struct field %s: %v", fieldName, err)
				}
			}
		}
	case reflect.Bool:
		if err := enc.writeBool(v.Bool()); err != nil {
			return fmt.Errorf("could not write %s: %v", v.Type().Name(), err)
		}
	case reflect.Uint8:
		if err := enc.writeUint8(uint8(v.Uint())); err != nil {
			return fmt.Errorf("could not write %s: %v", v.Type().Name(), err)
		}
	case reflect.Uint16:
		if err := enc.writeUint16(uint16(v.Uint())); err != nil {
			return fmt.Errorf("could not write %s: %v", v.Type().Name(), err)
		}
	case reflect.Uint32:
		if err := enc.writeUint32(uint32(v.Uint())); err != nil {
			return fmt.Errorf("could not write %s: %v", v.Type().Name(), err)
		}
	case reflect.Uint64:
		if err := enc.writeUint64(v.Uint()); err != nil {
			return fmt.Errorf("could not write %s: %v", v.Type().Name(), err)
		}
	case reflect.Int8:
		if err := enc.writeInt8(int8(v.Int())); err != nil {
			return fmt.Errorf("could not write %s: %v", v.Type().Name(), err)
		}
	case reflect.Int16:
		if err := enc.writeInt16(int16(v.Int())); err != nil {
			return fmt.Errorf("could not write %s: %v", v.Type().Name(), err)
		}
	case reflect.Int32:
		if err := enc.writeInt32(int32(v.Int())); err != nil {
			return fmt.Errorf("could not write %s: %v", v.Type().Name(), err)
		}
	case reflect.Int64:
		if err := enc.writeInt64(v.Int()); err != nil {
			return fmt.Errorf("could not write %s: %v", v.Type().Name(), err)
		}
	case reflect.String:
		ndrTag := parseTags(tag)
		conformant := ndrTag.HasValue(TagConformant)
		// strings are always varying so this is assumed without an explicit tag
		if conformant {
			if err := enc.writeConformantVaryingString(v.String()); err != nil {
				return fmt.Errorf("could not write conformant varying string: %v", err)
			}
		} else {
			if err := enc.writeVaryingString(v.String()); err != nil {
				return fmt.Errorf("could not write varying string: %v", err)
			}
		}
	case reflect.Float32:
		if err := enc.writeFloat32(float32(v.Float())); err != nil {
			return fmt.Errorf("could not write %v: %v", v.Type().Name(), err)
		}
	case reflect.Float64:
		if err := enc.writeFloat64(v.Float()); err != nil {
			return fmt.Errorf("could not write %v: %v", v.Type().Name(), err)
		}
	case reflect.Array:
		if err := enc.writeFixedArray(v, tag, localDef); err != nil {
			return err
		}
	case reflect.Slice:
		if v.Type().Implements(reflect.TypeOf(new(RawBytes)).Elem()) && v.Type().Elem().Kind() == reflect.Uint8 {
			// field is for rawbytes
			if err := enc.writeRawBytes(v, tag); err != nil {
				return fmt.Errorf("could not write raw bytes struct field: %v", err)
			}
			break
		}
		ndrTag := parseTags(tag)
		conformant := ndrTag.HasValue(TagConformant)
		varying := ndrTag.HasValue(TagVarying)
		if ndrTag.HasValue(TagPipe) {
			if err := enc.writePipe(v, tag); err != nil {
				return err
			}
			break
		}
		_, t := sliceDimensions(v.Type())
		if t.Kind() == reflect.String && !ndrTag.HasValue(subStringArrayValue) {
			// String array
			if err := enc.writeStringsArray(v, tag, localDef); err != nil {
				return err
			}
			break
		}
		// varying is assumed as fixed arrays use the Go array type rather than slice
		if conformant && varying {
			if err := enc.writeConformantVaryingArray(v, tag, localDef); err != nil {
				return err
			}
		} else if !conformant && varying {
			if err := enc.writeVaryingArray(v, tag, localDef); err != nil {
				return err
			}
		} else {
			// default to conformant and not varying
			if err := enc.writeConformantArray(v, tag, localDef); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unsupported type")
	}
	return nil
}

// writeBytes writes a number of bytes to the NDR byte stream.
func (enc *Encoder) writeBytes(b []byte) error {
	_, err := enc.buf.Write(b)
	if err != nil {
		return fmt.Errorf("error writing bytes to stream: %v", err)
	}
	return nil
}

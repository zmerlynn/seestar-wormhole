package ndr

import (
	"errors"
	"fmt"
	"reflect"
)

// precedingMax consumes the next hoisted conformant max value. The value was
// already written at the front of the structure by scanConformantArrays; here
// it is popped so the consume order mirrors the decoder.
func (enc *Encoder) precedingMax() uint32 {
	m := enc.conformantMax[0]
	enc.conformantMax = enc.conformantMax[1:]
	return m
}

// sliceDimLengths returns the length of each of the first d dimensions of a
// (possibly multi-dimensional) slice value. It is panic-safe for empty slices.
func sliceDimLengths(v reflect.Value, d int) []int {
	l := make([]int, d)
	for i := 0; i < d; i++ {
		if v.Kind() == reflect.Ptr {
			v = v.Elem()
		}
		if v.Kind() != reflect.Slice && v.Kind() != reflect.Array {
			break
		}
		l[i] = v.Len()
		if v.Len() == 0 {
			break
		}
		v = v.Index(0)
	}
	return l
}

// writeFixedArray establishes if the fixed array is uni or multi dimensional and writes it.
func (enc *Encoder) writeFixedArray(v reflect.Value, tag reflect.StructTag, def *[]deferedPtr) error {
	l, t := parseDimensions(v)
	if t.Kind() == reflect.String {
		tag = reflect.StructTag(subStringArrayTag)
	}
	if len(l) < 1 {
		return errors.New("could not establish dimensions of fixed array")
	}
	if len(l) == 1 {
		if err := enc.writeUniDimensionalFixedArray(v, tag, def); err != nil {
			return fmt.Errorf("could not write uni-dimensional fixed array: %v", err)
		}
		return nil
	}
	// Fixed array is multidimensional
	ps := multiDimensionalIndexPermutations(l[:len(l)-1])
	for _, p := range ps {
		// Get current multi-dimensional index to write
		a := v
		for _, i := range p {
			a = a.Index(i)
		}
		// write the last dimension array
		if err := enc.writeUniDimensionalFixedArray(a, tag, def); err != nil {
			return fmt.Errorf("could not write dimension %v of multi-dimensional fixed array: %v", p, err)
		}
	}
	return nil
}

// writeUniDimensionalFixedArray writes an array (not slice) to the byte stream.
func (enc *Encoder) writeUniDimensionalFixedArray(v reflect.Value, tag reflect.StructTag, def *[]deferedPtr) error {
	for i := 0; i < v.Len(); i++ {
		if err := enc.fill(v.Index(i), tag, def); err != nil {
			return fmt.Errorf("could not write index %d of fixed array: %v", i, err)
		}
	}
	return nil
}

// writeConformantArray establishes if the conformant array is uni or multi dimensional and writes the slice.
func (enc *Encoder) writeConformantArray(v reflect.Value, tag reflect.StructTag, def *[]deferedPtr) error {
	d, _ := sliceDimensions(v.Type())
	if d > 1 {
		return enc.writeMultiDimensionalConformantArray(v, d, tag, def)
	}
	return enc.writeUniDimensionalConformantArray(v, tag, def)
}

// writeUniDimensionalConformantArray writes the uni-dimensional slice value.
func (enc *Encoder) writeUniDimensionalConformantArray(v reflect.Value, tag reflect.StructTag, def *[]deferedPtr) error {
	// The max count was hoisted to the front of the structure; consume it.
	n := int(enc.precedingMax())
	for i := 0; i < n; i++ {
		if err := enc.fill(v.Index(i), tag, def); err != nil {
			return fmt.Errorf("could not write index %d of uni-dimensional conformant array: %v", i, err)
		}
	}
	return nil
}

// writeMultiDimensionalConformantArray writes the multi-dimensional slice value as conformant array data.
func (enc *Encoder) writeMultiDimensionalConformantArray(v reflect.Value, d int, tag reflect.StructTag, def *[]deferedPtr) error {
	// The max size of each dimension was hoisted to the front; consume them.
	l := make([]int, d)
	for i := range l {
		l[i] = int(enc.precedingMax())
	}
	// Write each element in the same permutation order the decoder reads.
	ps := multiDimensionalIndexPermutations(l)
	for _, p := range ps {
		a := v
		for _, i := range p {
			a = a.Index(i)
		}
		if err := enc.fill(a, tag, def); err != nil {
			return fmt.Errorf("could not write index %v of slice: %v", p, err)
		}
	}
	return nil
}

// writeVaryingArray establishes if the varying array is uni or multi dimensional and writes the slice.
func (enc *Encoder) writeVaryingArray(v reflect.Value, tag reflect.StructTag, def *[]deferedPtr) error {
	d, t := sliceDimensions(v.Type())
	if d > 1 {
		return enc.writeMultiDimensionalVaryingArray(v, t, d, tag, def)
	}
	return enc.writeUniDimensionalVaryingArray(v, tag, def)
}

// writeUniDimensionalVaryingArray writes the uni-dimensional slice value.
// The offset is always 0 and the actual count is the slice length.
func (enc *Encoder) writeUniDimensionalVaryingArray(v reflect.Value, tag reflect.StructTag, def *[]deferedPtr) error {
	if err := enc.writeUint32(0); err != nil {
		return fmt.Errorf("could not write offset of uni-dimensional varying array: %v", err)
	}
	if err := enc.writeUint32(uint32(v.Len())); err != nil {
		return fmt.Errorf("could not write actual count of uni-dimensional varying array: %v", err)
	}
	for i := 0; i < v.Len(); i++ {
		if err := enc.fill(v.Index(i), tag, def); err != nil {
			return fmt.Errorf("could not write index %d of uni-dimensional varying array: %v", i, err)
		}
	}
	return nil
}

// writeMultiDimensionalVaryingArray writes the multi-dimensional slice value as varying array data.
func (enc *Encoder) writeMultiDimensionalVaryingArray(v reflect.Value, t reflect.Type, d int, tag reflect.StructTag, def *[]deferedPtr) error {
	l := sliceDimLengths(v, d)
	// offset(0) + actual count per dimension
	for i := 0; i < d; i++ {
		if err := enc.writeUint32(0); err != nil {
			return fmt.Errorf("could not write offset of dimension %d: %v", i+1, err)
		}
		if err := enc.writeUint32(uint32(l[i])); err != nil {
			return fmt.Errorf("could not write actual count of dimension %d: %v", i+1, err)
		}
	}
	ps := multiDimensionalIndexPermutations(l)
	for _, p := range ps {
		a := v
		for _, j := range p {
			a = a.Index(j)
		}
		if err := enc.fill(a, tag, def); err != nil {
			return fmt.Errorf("could not write index %v of slice: %v", p, err)
		}
	}
	return nil
}

// writeConformantVaryingArray establishes if the conformant varying array is uni or multi dimensional and writes the slice.
func (enc *Encoder) writeConformantVaryingArray(v reflect.Value, tag reflect.StructTag, def *[]deferedPtr) error {
	d, t := sliceDimensions(v.Type())
	if d > 1 {
		return enc.writeMultiDimensionalConformantVaryingArray(v, t, d, tag, def)
	}
	return enc.writeUniDimensionalConformantVaryingArray(v, tag, def)
}

// writeUniDimensionalConformantVaryingArray writes the uni-dimensional slice value.
func (enc *Encoder) writeUniDimensionalConformantVaryingArray(v reflect.Value, tag reflect.StructTag, def *[]deferedPtr) error {
	// The max count was hoisted to the front of the structure; consume it.
	_ = enc.precedingMax()
	if err := enc.writeUint32(0); err != nil {
		return fmt.Errorf("could not write offset of uni-dimensional conformant varying array: %v", err)
	}
	if err := enc.writeUint32(uint32(v.Len())); err != nil {
		return fmt.Errorf("could not write actual count of uni-dimensional conformant varying array: %v", err)
	}
	for i := 0; i < v.Len(); i++ {
		if err := enc.fill(v.Index(i), tag, def); err != nil {
			return fmt.Errorf("could not write index %d of uni-dimensional conformant varying array: %v", i, err)
		}
	}
	return nil
}

// writeMultiDimensionalConformantVaryingArray writes the multi-dimensional slice value as conformant varying array data.
func (enc *Encoder) writeMultiDimensionalConformantVaryingArray(v reflect.Value, t reflect.Type, d int, tag reflect.StructTag, def *[]deferedPtr) error {
	// The max size of each dimension was hoisted to the front; consume them.
	m := make([]int, d)
	for i := range m {
		m[i] = int(enc.precedingMax())
	}
	l := sliceDimLengths(v, d)
	// offset(0) + actual count per dimension
	for i := 0; i < d; i++ {
		if err := enc.writeUint32(0); err != nil {
			return fmt.Errorf("could not write offset of dimension %d: %v", i+1, err)
		}
		if err := enc.writeUint32(uint32(l[i])); err != nil {
			return fmt.Errorf("could not write actual count of dimension %d: %v", i+1, err)
		}
	}
	// Write each element in the same permutation order the decoder reads.
	ps := multiDimensionalIndexPermutations(m)
	for _, p := range ps {
		a := v
		var skip bool
		for i, j := range p {
			if j >= l[i] {
				skip = true
				break
			}
			a = a.Index(j)
		}
		if skip {
			continue
		}
		if err := enc.fill(a, tag, def); err != nil {
			return fmt.Errorf("could not write index %v of slice: %v", p, err)
		}
	}
	return nil
}

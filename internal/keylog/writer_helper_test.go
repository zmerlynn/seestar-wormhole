package keylog

import "os"

func NewTestWriter(f *os.File) *Writer { return New(f, nil) }

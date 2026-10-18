package tracerecord

// Projector applies the recorder's exact allowlist and bounded aliases without
// storage, clocks or goroutines. Its zero value is ready for use by one owner.
// It must not be copied after first use.
type Projector struct{ writer Writer }

// Summarize discards byte values and assigns aliases in observation order.
// Unsupported input and alias overflow remain visible for incomplete evidence.
func (p *Projector) Summarize(input any) Summary {
	if p.writer.peers == nil {
		p.writer.peers = make(map[string]string)
		p.writer.ports = make(map[uint16]string)
	}
	return p.writer.summarize(input)
}

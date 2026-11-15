package main

import (
	"bytes"
	"strings"
	"testing"
)

// Held output is bounded, and written when startup is decided either way.
func TestStartupLogOutputDecisionAndBound(t *testing.T) {
	var output bytes.Buffer
	w := &startupLogOutput{output: &output}
	initial := []byte("dependency startup\n")
	w.Write(initial)
	if _, err := w.Write(bytes.Repeat([]byte("x"), startupLogLimit)); err == nil {
		t.Fatal("overflow succeeded")
	}
	if len(w.pending) > startupLogLimit {
		t.Fatal("startup output escaped its bound")
	}
	if strings.Contains(output.String(), "dependency") {
		t.Fatalf("written before the decision: %q", output.String())
	}
	w.resolve()
	w.resolve()
	w.Write([]byte("later\n"))
	if want := "dependency startup\nlater\n"; !strings.HasSuffix(output.String(), want) || w.pending != nil {
		t.Fatalf("output=%q pending=%d want=%q", output.String(), len(w.pending), want)
	}
}

type startupDrainSink struct {
	entered, release chan struct{}
	output           bytes.Buffer
}

func (s *startupDrainSink) Write(p []byte) (int, error) {
	if s.output.Len() == 0 {
		close(s.entered)
		<-s.release
	}
	return s.output.Write(p)
}
func TestStartupLogOutputConcurrentDrain(t *testing.T) {
	sink := &startupDrainSink{entered: make(chan struct{}), release: make(chan struct{})}
	w := &startupLogOutput{output: sink}
	w.Write([]byte("first"))
	done := make(chan struct{})
	go func() { w.resolve(); close(done) }()
	<-sink.entered
	w.Write([]byte("second"))
	close(sink.release)
	<-done
	w.Write([]byte("third"))
	if sink.output.String() != "firstsecondthird" {
		t.Fatalf("drain order: %q", sink.output.String())
	}
}

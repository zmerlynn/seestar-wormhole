package main

import (
	"bytes"
	"testing"
)

func TestStartupLogOutputDecisionAndBound(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		var output bytes.Buffer
		w := &startupLogOutput{output: &output}
		initial := []byte("dependency startup\n")
		w.Write(initial)
		w.Write(bytes.Repeat([]byte("x"), startupLogLimit))
		if len(w.pending) > startupLogLimit || output.Len() != 0 {
			t.Fatal("startup output escaped its bound or decision")
		}
		w.resolve(accepted)
		w.resolve(!accepted)
		w.Write([]byte("later\n"))
		want := ""
		if accepted {
			want = "dependency startup\nlater\n"
		}
		if output.String() != want || w.pending != nil {
			t.Fatalf("output=%q pending=%d want=%q", output.String(), len(w.pending), want)
		}
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
	go func() { w.resolve(true); close(done) }()
	<-sink.entered
	w.Write([]byte("second"))
	close(sink.release)
	<-done
	w.Write([]byte("third"))
	if sink.output.String() != "firstsecondthird" {
		t.Fatalf("drain order: %q", sink.output.String())
	}
}

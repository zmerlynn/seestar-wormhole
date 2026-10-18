package main

import (
	"io"
	"net"
	"testing"
	"time"
)

func TestDefaultBindLoopback(t *testing.T) {
	opts, err := parse(append([]string{"proxy"}, nil...), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort(opts.bind, "0"))
	if err != nil {
		t.Fatalf("default bind unavailable: %v", err)
	}
	defer listener.Close()
	conn, err := net.DialTimeout("tcp4", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("default loopback unreachable: %v", err)
	}
	conn.Close()
	accepted, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	accepted.Close()
}

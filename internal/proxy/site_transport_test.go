package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

func siteTransportPair(t *testing.T, port uint16) (*net.TCPConn, net.Conn) {
	t.Helper()
	device, local := listen(t), listen(t)
	t.Cleanup(func() { device.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, loopbackDialer{device.Addr().String()}, []Forward{{Listener: local, Port: port}}, 1, nil)
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	client := dial(t, local)
	t.Cleanup(func() { client.Close() })
	remote, err := device.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { remote.Close() })
	// Socket operations are completion gates; deadlines only watch for deadlock.
	client.SetDeadline(time.Now().Add(30 * time.Second))
	remote.SetDeadline(time.Now().Add(30 * time.Second))
	return client, remote
}

func TestSiteDeviceHalfCloseKeepsUpload(t *testing.T) {
	for _, method := range []string{"set_user_location", "pi_set_time"} {
		t.Run(method, func(t *testing.T) {
			client, remote := siteTransportPair(t, 4700)
			if err := remote.(*net.TCPConn).CloseWrite(); err != nil {
				t.Fatal(err)
			}
			var one [1]byte
			if n, err := client.Read(one[:]); n != 0 || err != io.EOF {
				t.Fatalf("device FIN: n=%d err=%v", n, err)
			}
			setter := `{"id":1,"method":"` + method + `","params":{"lat":1,"time_zone":"Etc/UTC"}}` + "\r\n"
			ordinary := `{"id":2,"method":"get_setting"}` + "\r\n"
			// Multiple dropped setters must not end the independent upload.
			if _, err := io.WriteString(client, setter+setter+ordinary); err != nil {
				t.Fatal(err)
			}
			if err := client.CloseWrite(); err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(remote)
			if string(got) != ordinary || err != nil {
				t.Fatalf("upload after device FIN=%q err=%v want=%q", got, err, ordinary)
			}
		})
	}
}

// Contract test: a binary prefix must arrive while both connections stay open.
func TestSiteBinaryPrefixImmediate(t *testing.T) {
	for _, port := range []uint16{80, 445, 4030, 4040, 4350, 4361, 4500, 4554, 4555, 4720, 4800, 4801, 4804, 54321} {
		for _, down := range []bool{false, true} {
			t.Run(fmt.Sprintf("port=%d/down=%t", port, down), func(t *testing.T) {
				client, remote := siteTransportPair(t, port)
				var src, dst net.Conn = client, remote
				if down {
					src, dst = remote, client
				}
				payload := []byte{0x89, 'P', 'N', 'G', 0, 1, 2, 3, 4, 5}
				if _, err := src.Write(payload); err != nil {
					t.Fatal(err)
				}
				got := make([]byte, len(payload))
				n, err := io.ReadFull(dst, got)
				if err != nil || !bytes.Equal(got, payload) {
					t.Fatalf("binary prefix with both connections open: received=%d/%d err=%v equal=%t", n, len(payload), err, bytes.Equal(got, payload))
				}
			})
		}
	}
}

// Contract test: >16KiB and embedded LF must not truncate or synthesize data.
func TestSiteBinaryTransferExact(t *testing.T) {
	for _, port := range []uint16{80, 445, 4030, 4040, 4350, 4361, 4500, 4554, 4555, 4720, 4800, 4801, 4804, 54321} {
		for _, down := range []bool{false, true} {
			t.Run(fmt.Sprintf("port=%d/down=%t", port, down), func(t *testing.T) {
				client, remote := siteTransportPair(t, port)
				payload := bytes.Repeat([]byte{0, 0xff, 0x80, 'B'}, (2*MaxSiteLine+128)/4)
				payload[17] = '\n' // An embedded LF must not become a local-reply boundary.
				payload = append(payload, '\r', '\n')
				var src, dst net.Conn = client, remote
				if down {
					src, dst = remote, client
				}
				if _, err := src.Write(payload); err != nil {
					t.Fatal(err)
				}
				if err := src.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(dst)
				if err != nil || !bytes.Equal(got, payload) {
					t.Fatalf("binary transfer: received=%d/%d err=%v equal=%t", len(got), len(payload), err, bytes.Equal(got, payload))
				}
			})
		}
	}
}

// A rejected-looking upload must not inject a local JSON error into a download.
func TestSiteBinaryDownloadNoLocalReply(t *testing.T) {
	for _, port := range []uint16{4040, 4350, 4361, 4500, 4720, 4800, 4801, 4804} {
		t.Run(fmt.Sprint(port), func(t *testing.T) {
			client, remote := siteTransportPair(t, port)
			upload := []byte{0, 0xff, '\r', '\n'}
			download := []byte{0x89, 'P', 'N', 'G', '\n', 0, 0xff, 0x80, 'B'}
			if _, err := client.Write(upload); err != nil {
				t.Fatal(err)
			}
			if err := client.CloseWrite(); err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(remote)
			if err != nil || !bytes.Equal(got, upload) {
				t.Errorf("upload=%x want=%x err=%v", got, upload, err)
			}
			if _, err := remote.Write(download); err != nil {
				t.Fatal(err)
			}
			if err := remote.(*net.TCPConn).CloseWrite(); err != nil {
				t.Fatal(err)
			}
			got, err = io.ReadAll(client)
			if err != nil || !bytes.Equal(got, download) {
				t.Fatalf("download=%x want=%x err=%v", got, download, err)
			}
		})
	}
}

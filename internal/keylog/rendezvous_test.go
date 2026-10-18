package keylog

import (
	"os"
	"testing"
)

func TestRendezvousLiteral(t *testing.T) {
	path := t.TempDir() + "/keys"
	w, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	err = w.Rendezvous(7, [8]byte{0, 1, 2, 3, 4, 5, 6, 7}, 3, []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}, [12]byte{16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27})
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	b, err := os.ReadFile(path)
	const want = "# IOTC_KEY 7 0001020304050607 3 000102030405060708090a0b0c0d0e0f 101112131415161718191a1b\n"
	if err != nil || string(b) != want {
		t.Fatal("rendezvous line differs from literal fixture", err)
	}
}

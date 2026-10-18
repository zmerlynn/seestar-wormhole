package proxy

import (
	"bufio"
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"
)

func TestComposedCorrelatesSplitAndCoalescedLines(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		s := simulatedSession(t, ctx, Config{}, &simulatedCloud{})
		defer s.Close()
		c, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(5 * time.Second))
		request := func(id int) string {
			return fmt.Sprintf("{\"id\":%d,\"method\":\"get_device_state\",\"params\":{\"keys\":[\"client\",\"camera\",\"second_camera\"]}}\r\n", id)
		}
		a := request(7)
		c.Write([]byte(a[:9]))
		c.Write([]byte(a[9:] + request(8)))
		r := bufio.NewReader(c)
		for _, id := range []int{7, 8} {
			b, err := r.ReadString('\n')
			if err != nil || checkResponse(b, id) != nil {
				t.Fatalf("correlated reply %d: %q %v", id, b, err)
			}
		}
	})
}

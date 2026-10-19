package session

import (
	"math"
	"testing"
)

func TestReservation(t *testing.T) {
	c := DefaultConfig()
	got, err := c.Reservation()
	if err != nil || got != c.Budgets.Total {
		t.Fatalf("reservation = %+v, %v; want %+v", got, err, c.Budgets.Total)
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Budgets.Total.Bytes-- },
		func(c *Config) { c.Budgets.Total.Count-- },
		func(c *Config) { c.Budgets.Layers[DTLS].Bytes = -1 },
		func(c *Config) { c.Budgets.Channel.Bytes = math.MaxInt },
		func(c *Config) { c.Budgets.Inbox.Count = math.MaxInt },
		func(c *Config) { c.MaxClients = 0 },
	} {
		c = DefaultConfig()
		mutate(&c)
		if _, err = c.Reservation(); err != ErrBudget {
			t.Fatalf("invalid reservation accepted: %+v", c)
		}
	}
}

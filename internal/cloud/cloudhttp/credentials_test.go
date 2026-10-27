package cloudhttp

import (
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"testing"
)

func TestInMemoryCredentialsIgnoreEnvironment(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "invalid")
	t.Setenv("SEESTAR_PASSWORD", "invalid")
	credentials, err := cloud.EmailCredentials("synthetic@example.invalid", "synthetic-password")
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewWithCredentials(Config{}, credentials)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if _, err := NewFromEnvironment(Config{}); err == nil {
		t.Fatal("environment constructor bypassed validation")
	}
}

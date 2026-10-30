package ui

import (
	"encoding/json/v2"
	"strings"
	"testing"
)

func TestNumericPortInputReturnsFieldRecovery(t *testing.T) {
	for _, action := range []string{"start", "password/save"} {
		for _, value := range []string{"1.5", "-1", "65536", `"invalid"`} {
			s := testServer(t)
			body := `{"settings":{"version":1,"bind":"127.0.0.1","idle":"0","email":"synthetic@example.invalid","ports":[{"device":80,"local":` + value + `}]},"password":"synthetic-password"}`
			w := doPost(s, "/api/"+action, body)
			var got struct {
				ActionError
				Message string `json:"message"`
			}
			if w.Code != 400 || json.Unmarshal(w.Body.Bytes(), &got) != nil {
				t.Fatal("unstructured numeric rejection", w.Code)
			}

			if got.Code != "ports" || got.Field != "ports" || got.Message != errorCopy("ports") || strings.Contains(got.Message, "controls") {
				t.Fatalf("wrong numeric recovery: %+v", got)
			}
			if s.controller.credentials.state(testSettings().Email).Saved {
				t.Fatal("invalid numeric request saved password")
			}
		}
	}
}

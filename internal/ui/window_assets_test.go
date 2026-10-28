package ui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWindowAssetsNeverReachWails(t *testing.T) {
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("Wails transport reached") })
	for _, path := range []string{"/", "/wails/runtime.js", "/wails/runtime?object=0&method=0", "/wails/flags", "/any-asset"} {
		for _, method := range []string{"GET", "POST"} {
			w := httptest.NewRecorder()
			blockWindowAssets(next).ServeHTTP(w, httptest.NewRequest(method, path, nil))
			if w.Code != http.StatusNotFound {
				t.Fatalf("%s %s: %d", method, path, w.Code)
			}
		}
	}
}

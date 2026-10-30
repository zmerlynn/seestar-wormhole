package ui

import (
	"context"
	"encoding/json/v2"
	"github.com/zalando/go-keyring"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// Suspend immediately before the provider gate is acquired. This models a
// descheduled HTTP handler without changing the provider or production code.
type reviewPausedContext struct {
	context.Context
	reached, resume chan struct{}
}

func (c reviewPausedContext) Deadline() (time.Time, bool) {
	close(c.reached)
	<-c.resume
	return time.Time{}, false
}

func TestReviewRemoveCannotBeUndoneByEarlierSave(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testServer(t)
		f := &fakeKeychain{}
		s.controller.credentials.chain = f
		body, _ := json.Marshal(input{testSettings(), testPassword})
		ctx := reviewPausedContext{context.Background(), make(chan struct{}), make(chan struct{})}
		saved := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			r := request(s, "POST", "/api/password/save", string(body)).WithContext(ctx)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			saved <- w
		}()
		<-ctx.reached
		if w := doPost(s, "/api/forget", string(body)); w.Code != 200 {
			t.Fatal("Remove failed")
		}
		if f.value != "" {
			t.Fatal("Remove did not delete")
		}
		close(ctx.resume)
		w := <-saved
		if w.Code == 200 {
			t.Fatal("superseded Save acknowledged")
		}
		if f.value != "" {
			t.Fatal("earlier Save recreated a secret after successful Remove")
		}
	})
}

// Delay only error classification, after Get has released the provider gate.
type reviewPausedError struct {
	once            sync.Once
	match           error
	reached, resume chan struct{}
}

func (e *reviewPausedError) Error() string { return "synthetic denial" }
func (e *reviewPausedError) Is(target error) bool {
	e.once.Do(func() { close(e.reached); <-e.resume })
	return e.match != nil && e.match == target
}

type reviewErrorKeychain struct{ err error }

func (f reviewErrorKeychain) Get(string, string) (string, error) { return "", f.err }
func (f reviewErrorKeychain) Set(string, string, string) error   { return nil }
func (f reviewErrorKeychain) Delete(string, string) error        { return nil }
func TestReviewRemoveSupersedesGetErrorPublication(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"denied", nil}, {"canceled", context.Canceled}, {"timeout", context.DeadlineExceeded}, {"missing", keyring.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				a := testSettings()
				a.Remember = true
				failure := &reviewPausedError{reached: make(chan struct{}), resume: make(chan struct{}), match: tc.err}
				c := &credentialStore{chain: reviewErrorKeychain{failure}, savedEmail: a.Email}
				get := make(chan error, 1)
				go func() { _, err := c.credentials(context.Background(), a, ""); get <- err }()
				<-failure.reached
				if err := c.forgetAt(context.Background(), a.Email, c.nextGeneration()); err != nil {
					t.Fatal(err)
				}
				if c.state(a.Email).Code != "password_removed" {
					t.Fatal("missing removal success")
				}
				close(failure.resume)
				<-get
				if c.state(a.Email).Code != "password_removed" {
					t.Fatal("older failed Get replaced successful Remove feedback")
				}
			})
		})
	}
}

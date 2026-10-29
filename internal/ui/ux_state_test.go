package ui

import (
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/zalando/go-keyring"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func TestRemoveFailureCanRetryRepeatedly(t *testing.T) {
	chain := &fakeKeychain{value: testPassword, err: errors.New("private")}
	c := &credentialStore{chain: chain, savedEmail: testSettings().Email, email: testSettings().Email, password: testPassword}
	for range 2 {
		if err := c.forget(t.Context(), testSettings().Email); err == nil {
			t.Fatal("denial claimed success")
		}
		state := c.state(testSettings().Email)
		if !state.Saved || state.Memory || state.Busy || state.Code != "password_remove" {
			t.Fatalf("retry unavailable: %+v", state)
		}
	}
	chain.err = nil
	if err := c.forget(t.Context(), testSettings().Email); err != nil {
		t.Fatal(err)
	}
	if state := c.state(testSettings().Email); state.Saved || state.Memory {
		t.Fatalf("not removed: %+v", state)
	}
	if err := c.forget(t.Context(), testSettings().Email); err != nil {
		t.Fatal("repeated removal", err)
	}
}
func TestConnectNeverWritesKeychain(t *testing.T) {
	chain := &fakeKeychain{}
	c := &credentialStore{chain: chain}
	s := testSettings()
	s.Remember = true
	if _, err := c.credentials(t.Context(), s, testPassword); err != nil {
		t.Fatal(err)
	}
	if chain.calls != 0 {
		t.Fatal("Connect wrote keychain")
	}
	s.Email = "new@example.invalid"
	s.Remember = false
	if _, err := c.credentials(t.Context(), s, ""); err == nil {
		t.Fatal("account inherited password")
	}
}
func TestExplicitPasswordSaveAssociationRelaunch(t *testing.T) {
	s := testServer(t)
	s.controller.save = s.saveSettings
	body := `{"settings":{"version":2,"bind":"127.0.0.1","idle":"0","email":"synthetic@example.invalid"},"password":"synthetic-password"}`
	w := doPost(s, "/api/password/save", body)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	saved, err := loadSettings(s.storage)
	if err != nil || !saved.Remember || saved.Email != testSettings().Email {
		t.Fatalf("association: %+v %v", saved, err)
	}
	loaded := &credentialStore{chain: s.controller.credentials.chain, savedEmail: saved.Email}
	if !loaded.state(saved.Email).Saved {
		t.Fatal("missing selected association")
	}
	if _, err := loaded.credentials(t.Context(), saved, ""); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(s.storage.root, "config.json"))
	if strings.Contains(string(b), testPassword) {
		t.Fatal("plaintext secret")
	}
}
func TestSettingsSaveBoundarySnapshotAndRetry(t *testing.T) {
	for _, lazy := range []bool{false, true} {
		t.Run(map[bool]string{false: "eager", true: "lazy"}[lazy], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				armed := make(chan struct{})
				release := make(chan struct{})
				c := testController(func(ctx context.Context, _ Settings, _ cloud.Credentials, status *proxy.Status) error {
					<-armed
					status.MarkArmed()
					<-release
					return errors.New("private")
				})
				calls := 0
				c.save = func(got Settings) error {
					calls++
					if got.Email != testSettings().Email || got.Ports[0] != (proxyrun.Port{Local: 80, Device: 80}) {
						t.Errorf("wrong snapshot: %+v", got)
					}
					if calls == 1 {
						return errStorage
					}
					return nil
				}
				s := testSettings()
				if lazy {
					s.Idle = "2m"
				}
				if err := c.start(s, testPassword); err != nil {
					t.Fatal(err)
				}
				s.Ports[0] = proxyrun.Port{Device: 80}
				synctest.Wait()
				if calls != 0 {
					t.Fatal("saved on acknowledgment")
				}
				close(armed)
				synctest.Wait()
				if lazy {
					if calls != 1 || !c.view().SaveFailed {
						t.Fatal("armed boundary missing")
					}
					if err := c.retrySave(); err != nil || calls != 2 || !c.view().Saved {
						t.Fatal("retry failed", err)
					}
				} else if calls != 0 {
					t.Fatal("eager saved before Ready")
				}
				close(release)
				synctest.Wait()
				c.quit()
			})
		})
	}
}
func TestLogsSanitizedBoundedAndRotated(t *testing.T) {
	st := testStorage(t)
	log := &eventLog{storage: st}
	log.add("private token http://example.invalid/account device payload")
	if lines, _, _ := log.snapshot(); len(lines) != 0 {
		t.Fatal("untrusted event retained")
	}
	for i := 0; i < 1500; i++ {
		log.add("Connecting to your telescope…")
		log.add(errorCopy("signin"))
	}
	lines, _, failed := log.snapshot()
	if failed || len(lines) != eventLimit {
		t.Fatalf("events=%d failed=%v", len(lines), failed)
	}
	for _, name := range []string{"gui.log", "gui.log.1"} {
		info, err := os.Stat(filepath.Join(st.root, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > logLimit {
			t.Fatalf("%s: %v", name, info)
		}
		private, err := privatefile.OpenRead(filepath.Join(st.root, name))
		if err != nil {
			t.Fatal("unsafe private log", err)
		}
		private.Close()
		b, _ := os.ReadFile(filepath.Join(st.root, name))
		if strings.Contains(string(b), "private") {
			t.Fatal("private text leaked")
		}
	}
}
func TestRemoveTimeoutDoesNotConfirmDeletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		chain := &blockedDelete{release: make(chan struct{})}
		c := &credentialStore{chain: chain, savedEmail: testSettings().Email}
		err := c.forget(t.Context(), testSettings().Email)
		if actionError(err).Code != "keychain_timeout" || !c.state(testSettings().Email).Saved || !c.state(testSettings().Email).Busy {
			t.Fatal("uncertain deletion misrepresented")
		}
		close(chain.release)
		synctest.Wait()
		if c.state(testSettings().Email).Busy {
			t.Fatal("native operation did not reconcile")
		}
		if err := c.forget(t.Context(), testSettings().Email); err != nil {
			t.Fatal(err)
		}
	})
}

type blockedDelete struct{ release chan struct{} }

func (f *blockedDelete) Get(string, string) (string, error) { return testPassword, nil }
func (f *blockedDelete) Set(string, string, string) error   { return nil }
func (f *blockedDelete) Delete(string, string) error        { <-f.release; return nil }
func doPost(s *server, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.ServeHTTP(w, request(s, "POST", path, body))
	return w
}

func TestCanceledRunRetainsSignInFailure(t *testing.T) {
	err := errors.Join(context.Canceled, diagnostic.NewTerminal(session.Cloud, "unauthorized", session.ErrUncertain))
	if got := finalStatus(err); got != errorCopy("signin") {
		t.Fatalf("lost rejection: %s", got)
	}
}

func TestSaveTimeoutRetainsMemoryWithoutClaimingSaved(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		c := &credentialStore{chain: &blockedSave{release: release}}
		if err := c.remember(t.Context(), testSettings(), testPassword); actionError(err).Code != "keychain_timeout" {
			t.Fatal(err)
		}
		state := c.state(testSettings().Email)
		if state.Saved || !state.Memory || !state.Busy {
			t.Fatalf("save uncertainty: %+v", state)
		}
		close(release)
		synctest.Wait()
		if c.state(testSettings().Email).Saved {
			t.Fatal("late operation claimed association without acknowledged save")
		}
		if err := c.remember(t.Context(), testSettings(), ""); err != nil {
			t.Fatal(err)
		}
		if !c.state(testSettings().Email).Saved {
			t.Fatal("serialized retry did not save")
		}
	})
}

type blockedSave struct{ release chan struct{} }

func (f *blockedSave) Get(string, string) (string, error) { return "", keyring.ErrNotFound }
func (f *blockedSave) Set(string, string, string) error   { <-f.release; return nil }
func (f *blockedSave) Delete(string, string) error        { return nil }

func TestSavedReadFailureKeepsAssociationAndAcceptsTypedCorrection(t *testing.T) {
	for _, err := range []error{keyring.ErrNotFound, errors.New("private denial")} {
		chain := &fakeKeychain{err: err}
		c := &credentialStore{chain: chain, savedEmail: testSettings().Email}
		s := testSettings()
		s.Remember = true
		if _, err := c.credentials(t.Context(), s, ""); err == nil {
			t.Fatal("read failure accepted")
		}
		if !c.state(s.Email).Saved {
			t.Fatal("read failure removed association")
		}
		if _, err := c.credentials(t.Context(), s, testPassword); err != nil {
			t.Fatal("typed correction failed", err)
		}
		if chain.calls != 1 {
			t.Fatal("typed correction accessed keychain")
		}
	}
}
func TestPasswordSaveStorageFailureAndRetry(t *testing.T) {
	s := testServer(t)
	if err := os.Mkdir(filepath.Join(s.storage.root, "config.json"), 0700); err != nil {
		t.Fatal(err)
	}
	body := `{"settings":{"version":2,"bind":"127.0.0.1","idle":"0","email":"synthetic@example.invalid"},"password":"synthetic-password"}`
	if w := doPost(s, "/api/password/save", body); w.Code != 400 {
		t.Fatal("storage failure accepted")
	}
	if !s.controller.credentials.state(testSettings().Email).Saved || !s.controller.view().SaveFailed {
		t.Fatal("keychain outcome lost after storage failure")
	}
	if err := os.Remove(filepath.Join(s.storage.root, "config.json")); err != nil {
		t.Fatal(err)
	}
	if w := doPost(s, "/api/retry-storage", `{}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	saved, err := loadSettings(s.storage)
	if err != nil || !saved.Remember {
		t.Fatal("association retry failed", err)
	}
}

func TestStorageRetryKeepsSnapshotAfterAnotherAttempt(t *testing.T) {
	s := testServer(t)
	if err := os.Mkdir(filepath.Join(s.storage.root, "config.json"), 0700); err != nil {
		t.Fatal(err)
	}
	original := testSettings()
	if err := s.controller.persist(original); err == nil {
		t.Fatal("storage failure missing")
	}
	// A later failed Start updates page settings but must not replace the retry snapshot.
	s.settings.Email = "later-attempt@example.invalid"
	if err := os.Remove(filepath.Join(s.storage.root, "config.json")); err != nil {
		t.Fatal(err)
	}
	if w := doPost(s, "/api/retry-storage", `{}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	loaded, err := loadSettings(s.storage)
	if err != nil || loaded.Email != original.Email {
		t.Fatalf("retry saved later failed edits: %+v %v", loaded, err)
	}
}

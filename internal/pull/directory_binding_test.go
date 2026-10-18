package pull

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rclone/rclone/fs"
)

type inventoryFailureFs struct {
	fs.Fs
	list func(context.Context, string) (fs.DirEntries, error)
}

func (f inventoryFailureFs) List(ctx context.Context, dir string) (fs.DirEntries, error) {
	return f.list(ctx, dir)
}

type cancelPlanningObject struct {
	fs.Object
	cancel context.CancelFunc
}

func (o cancelPlanningObject) ModTime(ctx context.Context) time.Time {
	o.cancel()
	return o.Object.ModTime(ctx)
}

func TestDirectoryBindingSurvivesInventoryFailure(t *testing.T) {
	for _, rebind := range []bool{false, true} {
		for _, mode := range []string{"unsafe", "case", "future", "budget", "list-error", "cancel", "cancel-plan"} {
			t.Run(fmt.Sprintf("%s/rebind=%t", mode, rebind), func(t *testing.T) {
				c, d, src, root, now := fixture(t)
				d.State.Source = ""
				c.Dirs = []string{"MyWorks"}
				if err := d.BindLayout(c.Dirs); err != nil {
					t.Fatal(err)
				}
				if err := d.BindSource("test-source"); err != nil {
					t.Fatal(err)
				}
				putFile(t, root, "MyWorks/a.fit", "data", now.Add(-time.Hour))
				var before []byte
				statePath := filepath.Join(c.Destination, metadata, "state.json")
				if rebind {
					runBatch(t, d, c, src, now)
					var err error
					before, err = os.ReadFile(statePath)
					if err != nil {
						t.Fatal(err)
					}
					if err := d.RebindSource("new-source"); err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				want := "unsafe_path"
				switch mode {
				case "unsafe":
					putFile(t, root, "MyWorks/NUL.fit", "bad", now.Add(-time.Hour))
				case "case":
					putFile(t, root, "MyWorks/A.fit", "bad", now.Add(-time.Hour))
					want = "case_collision"
				case "future":
					putFile(t, root, "MyWorks/future.fit", "bad", time.Now().Add(time.Hour))
					want = "future_mtime"
				case "budget":
					c.Dirs = []string{"A", "B"}
					src = directoryBudgetFs{src}
					want = "inventory_limit"
				case "list-error", "cancel", "cancel-plan":
					base := src
					src = inventoryFailureFs{Fs: src, list: func(ctx context.Context, dir string) (fs.DirEntries, error) {
						if mode == "list-error" {
							return nil, fs.ErrorPermissionDenied
						}
						entries, err := base.List(ctx, dir)
						if mode == "cancel-plan" {
							for i, entry := range entries {
								if o, ok := entry.(fs.Object); ok {
									entries[i] = cancelPlanningObject{o, cancel}
								}
							}
							return entries, err
						}
						cancel()
						// Exercise a backend returning success as cancellation arrives.
						return entries, err
					}}
				}
				_, err := d.Run(ctx, c, now, src)
				switch mode {
				case "cancel", "cancel-plan":
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("err=%v want cancellation", err)
					}
				case "list-error":
					if !errors.Is(err, fs.ErrorPermissionDenied) {
						t.Fatalf("err=%v want list refusal", err)
					}
				default:
					requireReason(t, err, want)
				}
				after, err := os.ReadFile(statePath)
				if rebind {
					if err != nil || !bytes.Equal(before, after) {
						t.Fatalf("inventory failure changed saved binding: err=%v\nbefore=%s\nafter=%s", err, before, after)
					}
				} else if !os.IsNotExist(err) {
					t.Fatalf("inventory failure created state: err=%v state=%s", err, after)
				}
			})
		}
	}
}

func TestDirectoryBindingPersistsBeforeTransfer(t *testing.T) {
	for _, rebind := range []bool{false, true} {
		for _, saveFailure := range []bool{false, true} {
			t.Run(fmt.Sprintf("rebind=%t/saveFailure=%t", rebind, saveFailure), func(t *testing.T) {
				c, d, src, root, now := fixture(t)
				d.State.Source = ""
				c.Dirs = []string{"MyWorks"}
				if err := d.BindLayout(c.Dirs); err != nil {
					t.Fatal(err)
				}
				if err := d.BindSource("test-source"); err != nil {
					t.Fatal(err)
				}
				if rebind {
					putFile(t, root, "MyWorks/old.fit", "old", now.Add(-time.Hour))
					runBatch(t, d, c, src, now)
					if err := d.RebindSource("new-source"); err != nil {
						t.Fatal(err)
					}
				}
				putFile(t, root, "MyWorks/new.fit", "new", now.Add(-time.Hour))
				if saveFailure {
					statePath := filepath.Join(c.Destination, metadata, "state.json")
					if err := os.Remove(statePath); err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
					if err := os.Mkdir(statePath, 0700); err != nil {
						t.Fatal(err)
					}
				}
				reads := 0
				checked := injectedFs{src, func(_ context.Context, o fs.Object) fs.Object {
					return injectedObject{o, func(ctx context.Context, opts ...fs.OpenOption) (io.ReadCloser, error) {
						reads++
						saved, err := OpenReadOnlyDestination(c.Destination)
						if err != nil {
							return nil, fmt.Errorf("binding not readable before data: %w", err)
						}
						defer saved.Close()
						if saved.State.Source != d.State.Source || saved.State.Generation != d.State.Generation || saved.State.Root != shareRoot || len(saved.State.Selections) != 0 {
							return nil, errors.New("staged binding not saved before data")
						}
						return o.Open(ctx, opts...)
					}}
				}}
				b, err := d.Run(t.Context(), c, now, checked)
				if saveFailure {
					if err == nil || reads != 0 {
						t.Fatalf("save failure: err=%v reads=%d want failure before data", err, reads)
					}
					if _, err := os.Stat(filepath.Join(c.Destination, "MyWorks", "new.fit")); !os.IsNotExist(err) {
						t.Fatalf("save failure wrote data: %v", err)
					}
					return
				}
				if err != nil || reads == 0 {
					t.Fatalf("transfer err=%v reads=%d", err, reads)
				}
				if err := b.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				saved, err := OpenReadOnlyDestination(c.Destination)
				if err != nil {
					t.Fatal(err)
				}
				defer saved.Close()
				receipt := saved.State.Files["MyWorks/new.fit"]
				if receipt.Source != d.State.Source || receipt.Generation != d.State.Generation || len(saved.State.Selections) != 1 {
					t.Fatalf("new receipt/checkpoint: %+v", saved.State)
				}
			})
		}
	}
}

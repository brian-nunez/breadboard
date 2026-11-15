package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brian-nunez/breadboard/internal/errs"
)

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

func TestWriteFileIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Crash after the new data is fully written but before the rename.
	crash := errors.New("simulated crash")
	beforeRename = func(string) error { return crash }
	t.Cleanup(func() { beforeRename = func(string) error { return nil } })
	if err := WriteFile(path, []byte("new and much longer"), 0o600); !errors.Is(err, crash) {
		t.Fatalf("WriteFile error = %v, want the simulated crash", err)
	}

	data, err := os.ReadFile(path)
	if err != nil || string(data) != "old" {
		t.Fatalf("after an interrupted write the file holds %q (%v), want the old content", data, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}

	beforeRename = func(string) error { return nil }
	if err := WriteFile(path, []byte("new"), 0o640); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "new" {
		t.Fatalf("content = %q", data)
	}
	if m := mode(t, path); m != 0o640 {
		t.Fatalf("mode = %o", m)
	}
}

func TestEnsureCreatesPrivateLayout(t *testing.T) {
	p := Project{Dir: t.TempDir()}
	created, err := p.Ensure("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("Ensure reported .bb as existing")
	}
	for path, want := range map[string]os.FileMode{
		p.StateDir():                             0o700,
		filepath.Join(p.StateDir(), "certs"):     0o700,
		p.RuntimeDir():                           0o700,
		filepath.Join(p.StateDir(), stateFile):   0o600,
		filepath.Join(p.StateDir(), secretsFile): 0o600,
	} {
		if got := mode(t, path); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}
	ignore, err := os.ReadFile(filepath.Join(p.StateDir(), ".gitignore"))
	if err != nil || !strings.Contains(string(ignore), "\n*\n") {
		t.Errorf(".gitignore = %q (%v), want a line with *", ignore, err)
	}
	st, err := p.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if st.FormatVersion != StateFormat || st.BBVersion != "v1.2.3" || st.ExperimentID == "" {
		t.Errorf("state = %+v", st)
	}
	if err := p.WriteCert("kafka", []byte("cert"), []byte("key")); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, filepath.Join(p.StateDir(), "certs", "kafka", "tls.key")); got != 0o600 {
		t.Errorf("key mode = %o", got)
	}
	created, err = p.Ensure("v1.2.3")
	if err != nil || created {
		t.Errorf("second Ensure: created=%v err=%v", created, err)
	}
}

func TestNewerFormatIsRefused(t *testing.T) {
	p := Project{Dir: t.TempDir()}
	if _, err := p.Ensure("v1"); err != nil {
		t.Fatal(err)
	}
	future := []byte(`{"formatVersion": 99, "services": {}}`)
	if err := os.WriteFile(filepath.Join(p.StateDir(), secretsFile), future, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.LoadSecrets(); !errors.Is(err, errs.ErrNewerFormat) {
		t.Fatalf("LoadSecrets error = %v, want ErrNewerFormat", err)
	}
	if err := p.SaveSecrets(&Secrets{}); !errors.Is(err, errs.ErrNewerFormat) {
		t.Fatalf("SaveSecrets error = %v, want ErrNewerFormat", err)
	}
	if data, _ := os.ReadFile(filepath.Join(p.StateDir(), secretsFile)); string(data) != string(future) {
		t.Fatal("a newer file was overwritten")
	}
}

func TestOlderFormatUpgradesInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.json")
	if err := os.WriteFile(path, []byte(`{"formatVersion": 1, "name": "a"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls []int
	steps := map[int]upgradeStep{
		1: func(m map[string]any) error {
			calls = append(calls, 1)
			m["name"] = m["name"].(string) + "b"
			return nil
		},
		2: func(m map[string]any) error {
			calls = append(calls, 2)
			m["title"] = m["name"]
			delete(m, "name")
			return nil
		},
	}
	var out struct {
		FormatVersion int    `json:"formatVersion"`
		Title         string `json:"title"`
	}
	found, err := loadJSON(path, 3, steps, &out)
	if err != nil || !found {
		t.Fatalf("loadJSON: found=%v err=%v", found, err)
	}
	if out.FormatVersion != 3 || out.Title != "ab" || len(calls) != 2 || calls[0] != 1 || calls[1] != 2 {
		t.Fatalf("out=%+v calls=%v", out, calls)
	}
	if _, err := loadJSON(path, 3, map[int]upgradeStep{}, &out); err == nil {
		t.Fatal("loadJSON succeeded without an upgrade step")
	}
}

func TestSecondLockHolderIsBusy(t *testing.T) {
	p := Project{Dir: t.TempDir()}
	first, err := p.Lock("up")
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Lock("down")
	if !errors.Is(err, errs.ErrBusy) {
		t.Fatalf("second Lock error = %v, want ErrBusy", err)
	}
	var busy *BusyError
	if !errors.As(err, &busy) || busy.Operation != "up" || busy.PID != os.Getpid() {
		t.Fatalf("busy error = %#v, want the holder's operation and pid", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	again, err := p.Lock("down")
	if err != nil {
		t.Fatalf("Lock after release: %v", err)
	}
	_ = again.Release()
}

func TestSecretsRoundTrip(t *testing.T) {
	p := Project{Dir: t.TempDir()}
	if _, err := p.Ensure("v1"); err != nil {
		t.Fatal(err)
	}
	s, err := p.LoadSecrets()
	if err != nil {
		t.Fatal(err)
	}
	s.Set("postgres", "password", "pw")
	if err := p.SaveSecrets(s); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(p.StateDir(), secretsFile))
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil || raw["formatVersion"] != float64(SecretsFormat) {
		t.Fatalf("secrets.json = %s", data)
	}
	got, _ := p.LoadSecrets()
	if v, ok := got.Get("postgres", "password"); !ok || v != "pw" {
		t.Fatalf("Get = %q %v", v, ok)
	}
}

func TestHomeCA(t *testing.T) {
	h := Home{Dir: filepath.Join(t.TempDir(), "home")}
	if _, _, ok, err := h.LoadCA("root"); ok || err != nil {
		t.Fatalf("LoadCA on empty home: ok=%v err=%v", ok, err)
	}
	if err := h.SaveCA("root", []byte("cert"), []byte("key")); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, h.Dir); got != 0o700 {
		t.Errorf("home mode = %o", got)
	}
	if got := mode(t, filepath.Join(h.CADir(), "root.key")); got != 0o600 {
		t.Errorf("CA key mode = %o", got)
	}
	cert, key, ok, err := h.LoadCA("root")
	if !ok || err != nil || string(cert) != "cert" || string(key) != "key" {
		t.Fatalf("LoadCA = %q %q %v %v", cert, key, ok, err)
	}
}

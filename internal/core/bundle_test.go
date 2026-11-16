package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/brian-nunez/breadboard/internal/compose"
)

// Running containers bind-mount single files from the runtime directory, so
// rewriting an unchanged file would orphan the mount. writeBundle must keep
// unchanged files (same inode), rewrite changed ones, and drop stale ones.
func TestWriteBundleKeepsUnchangedFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")
	first := &compose.Bundle{Files: []compose.File{
		{Path: "ca.pem", Data: []byte("CA"), Mode: 0o644},
		{Path: "tls/redis/tls.crt", Data: []byte("old"), Mode: 0o644},
		{Path: "config/gone/x.conf", Data: []byte("x"), Mode: 0o644},
	}}
	if err := writeBundle(dir, first); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(filepath.Join(dir, "ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	second := &compose.Bundle{Files: []compose.File{
		{Path: "ca.pem", Data: []byte("CA"), Mode: 0o644},
		{Path: "tls/redis/tls.crt", Data: []byte("new"), Mode: 0o644},
	}}
	if err := writeBundle(dir, second); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(filepath.Join(dir, "ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Error("an unchanged file was replaced")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "tls/redis/tls.crt")); string(data) != "new" {
		t.Errorf("changed file = %q, want new", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "config")); !os.IsNotExist(err) {
		t.Error("a stale file's now-empty directories were left behind")
	}
}

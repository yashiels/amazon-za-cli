package persist

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestAtomicPermissionsAndSymlinkRefusal(t *testing.T) {
	dir := TempDir(t)
	path := filepath.Join(dir, "config", "watch.json")
	if err := Write(path, []string{"initial"}); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []string{"replaced"}); err != nil {
		t.Fatal(err)
	}
	var values []string
	if err := Read(path, &values); err != nil || len(values) != 1 || values[0] != "replaced" {
		t.Fatal("atomic replacement failed")
	}
	for p, mode := range map[string]os.FileMode{path: 0600, filepath.Dir(path): 0700} {
		stat, err := os.Stat(p)
		if err != nil || stat.Mode().Perm() != mode {
			t.Fatalf("permissions for %s", p)
		}
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if Write(link, []string{}) == nil || Read(link, &values) == nil {
		t.Fatal("followed file symlink")
	}
	parent := filepath.Join(dir, "parent")
	if err := os.Symlink(filepath.Dir(path), parent); err != nil {
		t.Fatal(err)
	}
	if Write(filepath.Join(parent, "new.json"), values) == nil {
		t.Fatal("followed parent symlink")
	}
	if _, err := Lock(link, time.Millisecond); err == nil {
		t.Fatal("followed lock symlink")
	}
}

func TestLockContention(t *testing.T) {
	path := filepath.Join(TempDir(t), "watch.json.lock")
	unlock, err := Lock(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	started := time.Now()
	if _, err := Lock(path, 50*time.Millisecond); err == nil {
		t.Fatal("concurrent lock allowed")
	}
	if time.Since(started) > time.Second {
		t.Fatal("lock wait was unbounded")
	}
}

package persist

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/yashiels/amazon-za-cli/internal/fault"
)

func storageError() error { return fault.New("storage_error", "unsafe or inaccessible storage") }

func Check(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return storageError()
	}
	for p := absolute; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return storageError()
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return storageError()
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	return nil
}

func Prepare(path string) error {
	if err := Check(path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return storageError()
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return storageError()
	}
	return Check(path)
}

func Read(path string, value any) error {
	if err := Check(path); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return os.ErrNotExist
	}
	if err != nil {
		return storageError()
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return storageError()
	}
	if err := file.Chmod(0600); err != nil {
		return storageError()
	}
	if err := json.NewDecoder(file).Decode(value); err != nil {
		return storageError()
	}
	return nil
}

func Write(path string, value any) error {
	if err := Prepare(path); err != nil {
		return err
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return storageError()
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".amazon-*")
	if err != nil {
		return storageError()
	}
	defer func() { _ = os.Remove(file.Name()) }()
	failed := func() error { _ = file.Close(); return storageError() }
	if err := file.Chmod(0600); err != nil {
		return failed()
	}
	if _, err := file.Write(append(b, '\n')); err != nil {
		return failed()
	}
	if err := file.Sync(); err != nil {
		return failed()
	}
	if err := file.Close(); err != nil {
		return storageError()
	}
	if err := Check(path); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return storageError()
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return storageError()
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil {
		return storageError()
	}
	return nil
}

func Lock(path string, wait time.Duration) (func(), error) {
	if err := Prepare(path); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, storageError()
	}
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return nil, storageError()
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, storageError()
		}
		if time.Now().After(deadline) {
			_ = file.Close()
			return nil, fault.New("locked", "watchlist locked")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

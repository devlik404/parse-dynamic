//go:build !darwin && !freebsd && !linux && !netbsd && !openbsd

package securefile

import (
	"fmt"
	"os"
)

// Platforms without O_NOFOLLOW get a best-effort lstat/stat boundary.
func OpenRead(path string) (*os.File, error) {
	if err := rejectSymlink(path); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return requireRegular(file, path)
}

func OpenAppend(path string) (*os.File, error) {
	if _, err := os.Lstat(path); err == nil {
		if err := rejectSymlink(path); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	return requireRegular(file, path)
}

func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("path %q must not be a symbolic link", path)
	}
	return nil
}

func requireRegular(file *os.File, path string) (*os.File, error) {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("opened path %q is not a regular file", path)
	}
	return file, nil
}

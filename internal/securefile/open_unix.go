//go:build darwin || freebsd || linux || netbsd || openbsd

package securefile

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func OpenRead(path string) (*os.File, error) {
	return openUnix(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0, false)
}

func OpenAppend(path string) (*os.File, error) {
	return openUnix(path, unix.O_WRONLY|unix.O_CREAT|unix.O_APPEND|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600, true)
}

func openUnix(path string, flags int, mode uint32, enforcePrivate bool) (*os.File, error) {
	fd, err := unix.Open(path, flags, mode)
	if err != nil {
		return nil, fmt.Errorf("securely open %q: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("securely open %q: invalid file descriptor", path)
	}
	fail := func(cause error) (*os.File, error) {
		_ = file.Close()
		return nil, cause
	}
	info, err := file.Stat()
	if err != nil {
		return fail(fmt.Errorf("inspect opened file %q: %w", path, err))
	}
	if !info.Mode().IsRegular() {
		return fail(fmt.Errorf("opened path %q is not a regular file", path))
	}
	// O_NONBLOCK is required during open so special files such as FIFOs cannot
	// make validation hang. Once fstat has proved this is a regular file, clear
	// it so callers receive normal blocking file semantics.
	if err := unix.SetNonblock(fd, false); err != nil {
		return fail(fmt.Errorf("restore blocking mode on %q: %w", path, err))
	}
	if enforcePrivate {
		if err := unix.Fchmod(fd, 0o600); err != nil {
			return fail(fmt.Errorf("enforce private permissions on %q: %w", path, err))
		}
	}
	return file, nil
}

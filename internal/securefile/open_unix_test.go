//go:build darwin || freebsd || linux || netbsd || openbsd

package securefile

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestUnixOpenRejectsFIFOWithoutBlocking(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "named-pipe")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("create FIFO: %v", err)
	}

	tests := []struct {
		name string
		open func(string) (*os.File, error)
	}{
		{name: "read", open: OpenRead},
		{name: "append", open: OpenAppend},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := make(chan error, 1)
			go func() {
				file, err := test.open(fifo)
				if file != nil {
					_ = file.Close()
				}
				result <- err
			}()

			select {
			case err := <-result:
				if err == nil {
					t.Fatal("FIFO was accepted as a regular file")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("opening FIFO blocked")
			}
		})
	}
}

func TestUnixOpenRestoresBlockingModeForRegularFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "regular")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		open func(string) (*os.File, error)
	}{
		{name: "read", open: OpenRead},
		{name: "append", open: OpenAppend},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			file, err := test.open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()

			flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
			if err != nil {
				t.Fatalf("inspect descriptor flags: %v", err)
			}
			if flags&unix.O_NONBLOCK != 0 {
				t.Fatalf("regular file descriptor retained O_NONBLOCK: flags=%#x", flags)
			}
		})
	}
}

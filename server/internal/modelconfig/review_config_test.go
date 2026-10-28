package modelconfig

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestReviewE3ConfigRejectsNonRegularFileWithoutWaitingForBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-json-file")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	// Keep the FIFO open so Load can open it, then prove it waits for bytes
	// rather than rejecting a non-file input. No external process is involved.
	hold, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()
	done := make(chan error, 1)
	go func() { _, err := Load(path); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("non-regular configuration path was accepted")
		}
	case <-time.After(time.Second):
		hold.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		t.Fatal("explicit JSON config loading blocked on a FIFO rather than rejecting the non-regular input")
	}
}

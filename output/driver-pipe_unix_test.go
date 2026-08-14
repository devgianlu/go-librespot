//go:build test_unit && !windows

package output

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// eofReader is a Float32Reader that reports end of stream, so the pipe output
// loop goes idle and blocks on the cond var instead of writing.
type eofReader struct{}

func (eofReader) Read([]float32) (int, error) {
	return 0, io.EOF
}

func mkfifo(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test-fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	return path
}

func pipeOutputOptions(path string, waitForReader bool) *NewOutputOptions {
	return &NewOutputOptions{
		Reader:                  eofReader{},
		OutputPipe:              path,
		OutputPipeFormat:        "s16le",
		OutputPipeWaitForReader: waitForReader,
	}
}

func TestPipeOutputFailsWithoutReader(t *testing.T) {
	out, err := newPipeOutput(pipeOutputOptions(mkfifo(t), false))
	if err == nil {
		out.Close()
		t.Fatal("expected an error opening the FIFO without a reader")
	}
}

func TestPipeOutputWaitsForReader(t *testing.T) {
	path := mkfifo(t)

	result := make(chan *pipeOutput, 1)
	errCh := make(chan error, 1)
	go func() {
		out, err := newPipeOutput(pipeOutputOptions(path, true))
		if err != nil {
			errCh <- err
			return
		}
		result <- out
	}()

	// The open must wait for a reader instead of failing.
	select {
	case out := <-result:
		out.Close()
		t.Fatal("expected newPipeOutput to wait for a reader")
	case err := <-errCh:
		t.Fatalf("newPipeOutput returned error: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	// Connect a reader: the pending open should now complete.
	reader, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("failed opening FIFO for reading: %v", err)
	}
	defer reader.Close()

	var out *pipeOutput
	select {
	case out = <-result:
	case err := <-errCh:
		t.Fatalf("newPipeOutput returned error: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for newPipeOutput to open the FIFO")
	}

	defer out.Close()
}

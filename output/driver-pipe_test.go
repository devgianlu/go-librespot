//go:build test_unit

package output

import (
	"encoding/binary"
	"io"
	"math"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func s16(t *testing.T, in []float32) []int16 {
	t.Helper()

	transform, err := newPipeTransform("s16le")
	if err != nil {
		t.Fatalf("newPipeTransform: %v", err)
	}

	buf := make([]byte, len(in)*2)
	if n := transform(in, buf); n != len(in)*2 {
		t.Fatalf("transform wrote %d bytes, want %d", n, len(in)*2)
	}

	out := make([]int16, len(in))
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(buf[i*2:]))
	}
	return out
}

func s32(t *testing.T, in []float32) []int32 {
	t.Helper()

	transform, err := newPipeTransform("s32le")
	if err != nil {
		t.Fatalf("newPipeTransform: %v", err)
	}

	buf := make([]byte, len(in)*4)
	if n := transform(in, buf); n != len(in)*4 {
		t.Fatalf("transform wrote %d bytes, want %d", n, len(in)*4)
	}

	out := make([]int32, len(in))
	for i := range out {
		out[i] = int32(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	return out
}

func TestPipeTransformS16LEBounds(t *testing.T) {
	cases := []struct {
		in   float32
		want int16
	}{
		{0, 0},
		{0.5, 16384},
		{-0.5, -16384},
		{maxSampleValueS16, 32767},
		{1, 32767},
		{1.0001, 32767},
		{1.02, 32767},
		{2, 32767},
		{100, 32767},
		{float32(math.Inf(1)), 32767},
		{-1, -32768},
		{-1.02, -32768},
		{-2, -32768},
		{float32(math.Inf(-1)), -32768},
	}

	for _, c := range cases {
		if got := s16(t, []float32{c.in})[0]; got != c.want {
			t.Errorf("s16le(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestPipeTransformS32LEBounds(t *testing.T) {
	cases := []struct {
		in   float32
		want int32
	}{
		{0, 0},
		{0.5, 1073741823},
		{1, 2147483647},
		{1.02, 2147483647},
		{100, 2147483647},
		{-1, -2147483647},
		{-1.02, -2147483647},
		{-100, -2147483647},
	}

	for _, c := range cases {
		if got := s32(t, []float32{c.in})[0]; got != c.want {
			t.Errorf("s32le(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

// A wrap shows up as a non-monotonic step.
func TestPipeTransformMonotonic(t *testing.T) {
	const steps = 4001

	in := make([]float32, steps)
	for i := range in {
		in[i] = -2 + 4*float32(i)/float32(steps-1)
	}

	out := s16(t, in)
	for i := 1; i < len(out); i++ {
		if out[i] < out[i-1] {
			t.Fatalf("non-monotonic at %v -> %v: %d -> %d (wrapped)",
				in[i-1], in[i], out[i-1], out[i])
		}
	}

	if out[0] != -32768 || out[len(out)-1] != 32767 {
		t.Errorf("sweep endpoints = %d..%d, want -32768..32767", out[0], out[len(out)-1])
	}
}

func TestPipeTransformInRangeUnchanged(t *testing.T) {
	const steps = 2001

	in := make([]float32, steps)
	for i := range in {
		in[i] = -1 + 2*maxSampleValueS16*float32(i)/float32(steps-1)
	}

	out := s16(t, in)
	for i, v := range in {
		if want := int16(v * 32768); out[i] != want {
			t.Errorf("s16le(%v) = %d, want %d (unclamped path changed)", v, out[i], want)
		}
	}
}

func TestPipeTransformUnknownFormat(t *testing.T) {
	if _, err := newPipeTransform("s24le"); err == nil {
		t.Fatal("expected an error for an unsupported format")
	}
}

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

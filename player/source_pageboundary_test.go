package player

import (
	"bytes"
	"testing"
)

// makeOggPage builds a syntactically valid Ogg page with the given header
// type flag and payload (treated as a single packet). Granule position,
// serial number, sequence number, and CRC are left zero: the framing tracker
// only reads the capture pattern and the segment table.
func makeOggPage(headerType byte, payload []byte) []byte {
	var lacing []byte
	rem := len(payload)
	for {
		if rem >= 255 {
			lacing = append(lacing, 255)
			rem -= 255
			continue
		}
		lacing = append(lacing, byte(rem))
		break
	}

	page := make([]byte, 0, oggHeaderFixedSize+len(lacing)+len(payload))
	page = append(page, "OggS"...)           // capture pattern
	page = append(page, 0)                   // version
	page = append(page, headerType)          // header type flag
	page = append(page, make([]byte, 20)...) // granule, serial, sequence, crc
	page = append(page, byte(len(lacing)))   // segment count
	page = append(page, lacing...)           // segment table
	page = append(page, payload...)          // payload
	return page
}

// concat joins byte slices into a fresh slice.
func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// readExactly reads exactly n bytes from the switcher's passthrough path.
func readExactly(t *testing.T, s *SwitchingAudioSource, n int) []byte {
	t.Helper()
	out := make([]byte, 0, n)
	buf := make([]byte, n)
	for len(out) < n {
		nn, err := s.ReadBytes(buf[:n-len(out)])
		if err != nil {
			t.Fatalf("unexpected error reading %d bytes: %v", n, err)
		}
		out = append(out, buf[:nn]...)
	}
	return out
}

// expectBOSStart asserts that b starts with an Ogg page header carrying the
// beginning-of-stream flag (header_type 0x02), i.e. a new track's first page.
func expectBOSStart(t *testing.T, b []byte) {
	t.Helper()
	if len(b) < 6 {
		t.Fatalf("too short for an Ogg header: %d bytes", len(b))
	}
	if string(b[:4]) != "OggS" {
		t.Fatalf("expected an OggS capture pattern, got %q", b[:4])
	}
	if b[5] != 0x02 {
		t.Fatalf("expected header_type 0x02 (BOS), got %#x", b[5])
	}
}

// TestPassthroughSourceFramingTinyReads walks a two-page stream one byte at a
// time and verifies MidPage is true everywhere except exactly at the page
// boundaries, even though every page header straddles many ReadBytes calls.
func TestPassthroughSourceFramingTinyReads(t *testing.T) {
	page1 := makeOggPage(0x02, []byte("hello"))
	page2 := makeOggPage(0x04, bytes.Repeat([]byte{'x'}, 300))
	data := concat(page1, page2)

	src := newPassthroughSource(bytes.NewReader(data), int64(len(data)), 1000)

	if src.MidPage() {
		t.Fatal("MidPage must be false before any byte was read")
	}
	if rem := src.PageBytesRemaining(); rem != 0 {
		t.Fatalf("PageBytesRemaining at start = %d, want 0", rem)
	}

	buf := make([]byte, 1)
	for off := 0; off < len(data); off++ {
		if n, err := src.ReadBytes(buf); n != 1 || err != nil {
			t.Fatalf("read at offset %d: n=%d err=%v", off, n, err)
		}

		wantMid := off+1 != len(page1) && off+1 != len(data)
		if src.MidPage() != wantMid {
			t.Fatalf("MidPage after %d bytes = %v, want %v", off+1, !wantMid, wantMid)
		}
		if wantMid && src.PageBytesRemaining() <= 0 {
			t.Fatalf("PageBytesRemaining after %d bytes must be positive mid-page", off+1)
		}
	}
}

// TestReadBytesSwapMidPageDrainsToBoundary parks a SetPrimary that arrives
// mid-page: the old source must drain exactly to its current page boundary
// (never starting its next page) and the very next bytes must be the new
// source's BOS page.
func TestReadBytesSwapMidPageDrainsToBoundary(t *testing.T) {
	page1 := makeOggPage(0x02, bytes.Repeat([]byte{'A'}, 600))
	page2 := makeOggPage(0x00, bytes.Repeat([]byte{'B'}, 600))
	oldData := concat(page1, page2)
	newData := concat(makeOggPage(0x02, []byte("new-bos")), makeOggPage(0x00, bytes.Repeat([]byte{'C'}, 300)))

	oldSrc := newPassthroughSource(bytes.NewReader(oldData), int64(len(oldData)), 1000)
	newSrc := newPassthroughSource(bytes.NewReader(newData), int64(len(newData)), 1000)

	s := NewSwitchingAudioSource(0)
	s.SetPrimary(oldSrc)

	// Stop mid page1 (well past its header), then request the swap.
	const pre = 100
	got := readExactly(t, s, pre)
	if !bytes.Equal(got, oldData[:pre]) {
		t.Fatal("pre-swap bytes mismatch")
	}
	s.SetPrimary(newSrc)

	s.cond.L.Lock()
	parked := s.pendingSet
	s.cond.L.Unlock()
	if !parked {
		t.Fatal("mid-page SetPrimary must park the incoming source")
	}

	// A single large read must be capped at the rest of page1: the drain may
	// never start page2, and the old page must be completed, not truncated.
	buf := make([]byte, 4*1024)
	n, err := s.ReadBytes(buf)
	if err != nil {
		t.Fatalf("unexpected drain error: %v", err)
	}
	if want := len(page1) - pre; n != want {
		t.Fatalf("drain read returned %d bytes, want exactly the rest of the page (%d)", n, want)
	}
	if !bytes.Equal(buf[:n], page1[pre:]) {
		t.Fatal("drained bytes do not match the rest of the old page")
	}

	// The daemon initiated this transition itself: no done signal.
	expectNotDone(t, s)

	// The next bytes must be the new track's BOS page; page2 of the old
	// source must never surface.
	rest := readBytesUntilEOF(t, s, 100)
	expectBOSStart(t, rest)
	if !bytes.Equal(rest, newData) {
		t.Fatalf("post-swap bytes mismatch: got %d bytes, want the new source's %d", len(rest), len(newData))
	}
}

// TestReadBytesSwapMidHeaderDrainsWholePage parks a swap while the old source
// stands inside a page header (the page's total length is not even known yet):
// the drain must still complete that whole page before promoting.
func TestReadBytesSwapMidHeaderDrainsWholePage(t *testing.T) {
	page1 := makeOggPage(0x02, bytes.Repeat([]byte{'A'}, 400))
	page2 := makeOggPage(0x04, bytes.Repeat([]byte{'B'}, 500))
	oldData := concat(page1, page2)
	newData := makeOggPage(0x02, []byte("next-track"))

	oldSrc := newPassthroughSource(bytes.NewReader(oldData), int64(len(oldData)), 1000)
	newSrc := newPassthroughSource(bytes.NewReader(newData), int64(len(newData)), 1000)

	s := NewSwitchingAudioSource(0)
	s.SetPrimary(oldSrc)

	// Stop 10 bytes into page2's header.
	pre := len(page1) + 10
	readExactly(t, s, pre)
	s.SetPrimary(newSrc)

	rest := readBytesUntilEOF(t, s, 64)
	want := concat(oldData[pre:], newData)
	if !bytes.Equal(rest, want) {
		t.Fatalf("mid-header swap output mismatch: got %d bytes, want %d (rest of page2 + new track)", len(rest), len(want))
	}
	expectBOSStart(t, rest[len(oldData)-pre:])
}

// TestReadBytesSwapAtPageBoundarySwitchesImmediately verifies that a swap
// requested exactly at a page boundary is performed synchronously: nothing is
// parked and the old source's remaining pages are dropped.
func TestReadBytesSwapAtPageBoundarySwitchesImmediately(t *testing.T) {
	page1 := makeOggPage(0x02, bytes.Repeat([]byte{'A'}, 200))
	page2 := makeOggPage(0x00, bytes.Repeat([]byte{'B'}, 200))
	oldData := concat(page1, page2)
	newData := makeOggPage(0x02, []byte("immediate"))

	oldSrc := newPassthroughSource(bytes.NewReader(oldData), int64(len(oldData)), 1000)
	newSrc := newPassthroughSource(bytes.NewReader(newData), int64(len(newData)), 1000)

	s := NewSwitchingAudioSource(0)
	s.SetPrimary(oldSrc)

	// Consume exactly page1, landing on the boundary.
	readExactly(t, s, len(page1))
	s.SetPrimary(newSrc)

	s.cond.L.Lock()
	parked := s.pendingSet
	current := s.source[s.which]
	s.cond.L.Unlock()
	if parked {
		t.Fatal("a swap at a page boundary must not be parked")
	}
	if current != newSrc {
		t.Fatal("a swap at a page boundary must promote the new source immediately")
	}

	rest := readBytesUntilEOF(t, s, 64)
	expectBOSStart(t, rest)
	if !bytes.Equal(rest, newData) {
		t.Fatal("expected only the new source's bytes after a boundary swap")
	}
}

// TestReadBytesOldSourceEOFMidDrainPromotes truncates the old source in the
// middle of a page (its header promises more payload than exists): when the
// old source runs out mid-drain, the parked source must be promoted
// immediately and the EOF must not surface to the caller.
func TestReadBytesOldSourceEOFMidDrainPromotes(t *testing.T) {
	full := makeOggPage(0x02, bytes.Repeat([]byte{'A'}, 600))
	trunc := full[:200] // header claims 600 payload bytes, data ends early
	newData := makeOggPage(0x02, []byte("rescued"))

	oldSrc := newPassthroughSource(bytes.NewReader(trunc), int64(len(trunc)), 1000)
	newSrc := newPassthroughSource(bytes.NewReader(newData), int64(len(newData)), 1000)

	s := NewSwitchingAudioSource(0)
	s.SetPrimary(oldSrc)

	const pre = 50
	readExactly(t, s, pre)
	s.SetPrimary(newSrc)

	s.cond.L.Lock()
	parked := s.pendingSet
	s.cond.L.Unlock()
	if !parked {
		t.Fatal("mid-page SetPrimary must park the incoming source")
	}

	// Draining serves what is left of the truncated old source, then hits its
	// EOF mid-page and must promote the parked source without surfacing an
	// error or a done signal.
	rest := readBytesUntilEOF(t, s, 64)
	want := concat(trunc[pre:], newData)
	if !bytes.Equal(rest, want) {
		t.Fatalf("EOF-mid-drain output mismatch: got %d bytes, want %d", len(rest), len(want))
	}
	expectBOSStart(t, rest[len(trunc)-pre:])
}

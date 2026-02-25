//go:build unix

package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestParseHandshake(t *testing.T) {
	tests := []struct {
		name               string
		line               string
		wantUser           string
		wantCols, wantRows uint16
		wantCmd            []string
	}{
		{"full handshake", "alice 120 40\n", "alice", 120, 40, nil},
		{"with command", "alice 120 40 vim\n", "alice", 120, 40, []string{"vim"}},
		{"with command and args", "alice 120 40 vim /etc/hosts\n", "alice", 120, 40, []string{"vim", "/etc/hosts"}},
		{"user only (backwards compat)", "bob\n", "bob", 80, 24, nil},
		{"empty line", "\n", "root", 80, 24, nil},
		{"extra whitespace", "  carol  200  50  \n", "carol", 200, 50, nil},
		{"just user with spaces", "  dave  \n", "dave", 80, 24, nil},
		{"invalid cols", "eve abc 30\n", "eve", 80, 30, nil},
		{"invalid rows", "frank 100 xyz\n", "frank", 100, 24, nil},
		{"zero dimensions", "gina 0 0\n", "gina", 0, 0, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user, cols, rows, cmd := parseHandshake(tt.line)
			if user != tt.wantUser {
				t.Errorf("user = %q, want %q", user, tt.wantUser)
			}
			if cols != tt.wantCols {
				t.Errorf("cols = %d, want %d", cols, tt.wantCols)
			}
			if rows != tt.wantRows {
				t.Errorf("rows = %d, want %d", rows, tt.wantRows)
			}
			if len(cmd) != len(tt.wantCmd) {
				t.Errorf("cmd = %v, want %v", cmd, tt.wantCmd)
			} else {
				for i := range cmd {
					if cmd[i] != tt.wantCmd[i] {
						t.Errorf("cmd[%d] = %q, want %q", i, cmd[i], tt.wantCmd[i])
					}
				}
			}
		})
	}
}

// readWithTimeout reads exactly len(buf) bytes from f, failing the test if
// the read doesn't complete within the timeout.
func readWithTimeout(t *testing.T, f *os.File, buf []byte, timeout time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(f, buf)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("read: %v", err)
		}
	case <-time.After(timeout):
		t.Fatal("read timed out")
	}
}

// The passthrough and partial-magic tests use os.Pipe instead of a PTY
// to avoid line-discipline issues that can cause reads to block.

func TestCopyWithResize_PassthroughOnly(t *testing.T) {
	input := []byte("hello world, this is normal data")
	src := bytes.NewReader(input)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	go copyWithResize(w, src)

	out := make([]byte, len(input))
	readWithTimeout(t, r, out, 2*time.Second)
	if !bytes.Equal(out, input) {
		t.Errorf("passthrough mismatch:\n got: %q\nwant: %q", out, input)
	}
}

func TestCopyWithResize_ResizeMessage(t *testing.T) {
	// Build a stream: "AB" + resize(100,50) + "CD"
	var buf bytes.Buffer
	buf.WriteString("AB")
	buf.Write(resizeMagic[:])
	binary.Write(&buf, binary.BigEndian, uint16(100))
	binary.Write(&buf, binary.BigEndian, uint16(50))
	buf.WriteString("CD")

	ptmx, pts, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer pts.Close()

	// Use a pipe to capture the data output (avoiding PTY line discipline),
	// but pass ptmx to copyWithResize so pty.Setsize works.
	dataR, dataW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer dataR.Close()
	defer dataW.Close()

	// We need a writer that writes data to the pipe but resize calls go to ptmx.
	// Since copyWithResize writes both data and calls Setsize on the same *os.File,
	// we use ptmx directly and read from pts, but set raw mode first.

	// Actually, let's just test data and resize separately.
	// For data correctness: use pipe.
	// For resize: use PTY.

	// Test resize with PTY:
	resizeOnly := make([]byte, 0, 8)
	resizeOnly = append(resizeOnly, resizeMagic[:]...)
	var dims [4]byte
	binary.BigEndian.PutUint16(dims[0:2], 100)
	binary.BigEndian.PutUint16(dims[2:4], 50)
	resizeOnly = append(resizeOnly, dims[:]...)

	go copyWithResize(ptmx, bytes.NewReader(resizeOnly))

	// Give it a moment to process.
	time.Sleep(100 * time.Millisecond)

	ws, err := pty.GetsizeFull(ptmx)
	if err != nil {
		t.Fatalf("GetsizeFull: %v", err)
	}
	if ws.Cols != 100 || ws.Rows != 50 {
		t.Errorf("pty size = %dx%d, want 100x50", ws.Cols, ws.Rows)
	}
	ptmx.Close()

	// Test data correctness with pipe:
	var buf2 bytes.Buffer
	buf2.WriteString("AB")
	buf2.Write(resizeMagic[:])
	binary.Write(&buf2, binary.BigEndian, uint16(100))
	binary.Write(&buf2, binary.BigEndian, uint16(50))
	buf2.WriteString("CD")

	go copyWithResize(dataW, &buf2)

	out := make([]byte, 4)
	readWithTimeout(t, dataR, out, 2*time.Second)
	if string(out) != "ABCD" {
		t.Errorf("got %q, want %q", out, "ABCD")
	}
}

func TestCopyWithResize_PartialMagicFlushed(t *testing.T) {
	// Send the first 2 bytes of magic followed by a non-matching byte.
	// The partial magic bytes should be flushed as regular data.
	var buf bytes.Buffer
	buf.Write(resizeMagic[:2])
	buf.WriteByte('X')

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	go copyWithResize(w, &buf)

	want := append(resizeMagic[:2], 'X')
	out := make([]byte, len(want))
	readWithTimeout(t, r, out, 2*time.Second)
	if !bytes.Equal(out, want) {
		t.Errorf("got %x, want %x", out, want)
	}
}

func TestCopyWithResize_SplitAcrossReads(t *testing.T) {
	// Simulate the resize message arriving across two separate reads
	// by using io.MultiReader with separate buffers.
	part1 := append([]byte("Z"), resizeMagic[:2]...)
	part2 := make([]byte, 0, 6)
	part2 = append(part2, resizeMagic[2:]...)
	var dims [4]byte
	binary.BigEndian.PutUint16(dims[0:2], 200)
	binary.BigEndian.PutUint16(dims[2:4], 60)
	part2 = append(part2, dims[:]...)
	part2 = append(part2, 'W')

	src := io.MultiReader(bytes.NewReader(part1), bytes.NewReader(part2))

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	go copyWithResize(w, src)

	out := make([]byte, 2) // "Z" + "W"
	readWithTimeout(t, r, out, 2*time.Second)
	if string(out) != "ZW" {
		t.Errorf("got %q, want %q", out, "ZW")
	}
}

func TestCopyWithResize_DimensionsSplitAcrossReads(t *testing.T) {
	// Magic is in one read, dimensions split across next reads.
	magicBuf := resizeMagic[:]
	var dims [4]byte
	binary.BigEndian.PutUint16(dims[0:2], 150)
	binary.BigEndian.PutUint16(dims[2:4], 45)

	src := io.MultiReader(
		bytes.NewReader(magicBuf),
		bytes.NewReader(dims[:2]),
		bytes.NewReader(dims[2:]),
		bytes.NewReader([]byte("OK")),
	)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	go copyWithResize(w, src)

	out := make([]byte, 2)
	readWithTimeout(t, r, out, 2*time.Second)
	if string(out) != "OK" {
		t.Errorf("got %q, want %q", out, "OK")
	}
}

func TestCopyWithResize_TrailingPartialMagicFlushedOnEOF(t *testing.T) {
	// Stream ends with a partial magic match — should be flushed as data.
	input := append([]byte("hi"), resizeMagic[:3]...)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	go copyWithResize(w, bytes.NewReader(input))

	out := make([]byte, len(input))
	readWithTimeout(t, r, out, 2*time.Second)
	if !bytes.Equal(out, input) {
		t.Errorf("got %x, want %x", out, input)
	}
}

func TestCopyWithResize_ResizeSetsSize(t *testing.T) {
	// Verify that a resize message actually changes the PTY size.
	var buf bytes.Buffer
	buf.Write(resizeMagic[:])
	binary.Write(&buf, binary.BigEndian, uint16(132))
	binary.Write(&buf, binary.BigEndian, uint16(43))

	ptmx, pts, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer ptmx.Close()
	defer pts.Close()

	go copyWithResize(ptmx, &buf)

	// Give it time to process.
	time.Sleep(100 * time.Millisecond)

	ws, err := pty.GetsizeFull(ptmx)
	if err != nil {
		t.Fatalf("GetsizeFull: %v", err)
	}
	if ws.Cols != 132 || ws.Rows != 43 {
		t.Errorf("pty size = %dx%d, want 132x43", ws.Cols, ws.Rows)
	}
}

//go:build unix

package main

import (
	"encoding/binary"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/creack/pty"
)

// resizeMagic is the 4-byte prefix for in-band resize messages.
var resizeMagic = [4]byte{0x01, 0x80, 0x01, 0x80}

// parseHandshake parses "<user> <cols> <rows>" from the handshake line.
// Falls back to 80x24 if dimensions are missing (backwards compat).
func parseHandshake(line string) (user string, cols, rows uint16) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) == 0 {
		return "root", 80, 24
	}
	user = fields[0]
	cols, rows = 80, 24
	if len(fields) >= 3 {
		if c, err := strconv.ParseUint(fields[1], 10, 16); err == nil {
			cols = uint16(c)
		}
		if r, err := strconv.ParseUint(fields[2], 10, 16); err == nil {
			rows = uint16(r)
		}
	}
	return user, cols, rows
}

// copyWithResize copies from src to the PTY, watching for the 4-byte resize
// magic prefix in the stream. When found, it reads the next 4 bytes as
// cols (BE uint16) + rows (BE uint16) and calls pty.Setsize.
func copyWithResize(ptmx *os.File, src io.Reader) {
	buf := make([]byte, 32*1024)
	// matchLen tracks how many bytes of resizeMagic we've matched at the
	// end of the current read.
	matchLen := 0

	for {
		n, err := src.Read(buf)
		if n > 0 {
			data := buf[:n]
			i := 0
			for i < len(data) {
				if data[i] == resizeMagic[matchLen] {
					matchLen++
					i++
					if matchLen == len(resizeMagic) {
						// We've matched the full magic prefix.
						// The next 4 bytes are the dimensions.
						matchLen = 0

						// Read the 4-byte dimension payload. It may be
						// partially in the current buffer or require
						// another read.
						var dimBuf [4]byte
						dimFilled := 0
						remaining := data[i:]
						if len(remaining) >= 4 {
							copy(dimBuf[:], remaining[:4])
							dimFilled = 4
							i += 4
						} else {
							copy(dimBuf[:], remaining)
							dimFilled = len(remaining)
							i += len(remaining)
							// Read the rest from src.
							for dimFilled < 4 {
								rn, rerr := src.Read(dimBuf[dimFilled:])
								dimFilled += rn
								if rerr != nil {
									return
								}
							}
						}

						cols := binary.BigEndian.Uint16(dimBuf[0:2])
						rows := binary.BigEndian.Uint16(dimBuf[2:4])
						_ = pty.Setsize(ptmx, &pty.Winsize{Cols: cols, Rows: rows})
					}
				} else if matchLen > 0 {
					// Partial match failed — flush the matched magic
					// bytes as regular data, then reprocess current byte.
					ptmx.Write(resizeMagic[:matchLen])
					matchLen = 0
					// Don't advance i; re-check this byte.
				} else {
					// No match in progress — find how far we can write
					// without hitting a potential magic start.
					start := i
					i++
					for i < len(data) && data[i] != resizeMagic[0] {
						i++
					}
					ptmx.Write(data[start:i])
				}
			}
		}
		if err != nil {
			// Flush any partial magic match as data.
			if matchLen > 0 {
				ptmx.Write(resizeMagic[:matchLen])
			}
			return
		}
	}
}

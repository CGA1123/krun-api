package main

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

func main() {
	apiURL := flag.String("api", "http://localhost:9191", "krun-api server URL")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: krun-exec [flags] <user>@<machine-id>\n\nFlags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(1)
	}

	user, machineID, ok := parseTarget(flag.Arg(0))
	if !ok {
		fmt.Fprintf(os.Stderr, "error: expected <user>@<machine-id>\n")
		os.Exit(1)
	}

	if err := run(*apiURL, user, machineID); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func parseTarget(s string) (user, machineID string, ok bool) {
	parts := strings.SplitN(s, "@", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// machineResponse is a minimal subset of the API response.
type machineResponse struct {
	ID             string `json:"id"`
	ExecSocketPath string `json:"exec_socket_path"`
}

func run(apiURL, user, machineID string) error {
	// 1. Get the machine to discover the exec socket path.
	resp, err := http.Get(apiURL + "/v1/machines/" + machineID)
	if err != nil {
		return fmt.Errorf("get machine: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("get machine: HTTP %d: %s", resp.StatusCode, body)
	}

	var machine machineResponse
	if err := json.NewDecoder(resp.Body).Decode(&machine); err != nil {
		return fmt.Errorf("decode machine: %w", err)
	}

	if machine.ExecSocketPath == "" {
		return fmt.Errorf("machine %s has no exec socket path (is it running with vminit?)", machineID)
	}

	// 2. Connect to the exec socket.
	conn, err := net.DialTimeout("unix", machine.ExecSocketPath, 5*time.Second)
	if err != nil {
		return fmt.Errorf("connect to exec socket: %w", err)
	}
	defer conn.Close()

	// 3. Get terminal size and send handshake: "<user> <cols> <rows>\n"
	stdinFd := int(os.Stdin.Fd())
	if !term.IsTerminal(stdinFd) {
		return fmt.Errorf("stdin is not a terminal")
	}

	cols, rows, err := term.GetSize(stdinFd)
	if err != nil {
		return fmt.Errorf("get terminal size: %w", err)
	}

	if _, err := fmt.Fprintf(conn, "%s %d %d\n", user, cols, rows); err != nil {
		return fmt.Errorf("send handshake: %w", err)
	}

	// 4. Put local terminal into raw mode.
	oldState, err := term.MakeRaw(stdinFd)
	if err != nil {
		return fmt.Errorf("make raw: %w", err)
	}
	defer term.Restore(stdinFd, oldState)

	// 5. Set up SIGWINCH handler for terminal resize forwarding.
	winchCh := make(chan os.Signal, 1)
	signal.Notify(winchCh, syscall.SIGWINCH)
	defer signal.Stop(winchCh)

	// 6. Bidirectional copy: local stdin/stdout <-> vsock connection.
	done := make(chan struct{}, 2)
	go func() {
		copyStdinWithResize(conn, os.Stdin, stdinFd, winchCh)
		if tc, ok := conn.(*net.UnixConn); ok {
			tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(os.Stdout, conn)
		done <- struct{}{}
	}()
	<-done

	return nil
}

// resizeMagic is the 4-byte prefix for in-band resize messages.
var resizeMagic = [4]byte{0x01, 0x80, 0x01, 0x80}

// copyStdinWithResize copies stdin to dst while also injecting 8-byte resize
// messages when SIGWINCH is received.
func copyStdinWithResize(dst io.Writer, stdin *os.File, fd int, winchCh <-chan os.Signal) {
	buf := make([]byte, 32*1024)
	for {
		select {
		case <-winchCh:
			if err := sendResize(dst, fd); err != nil {
				return
			}
		default:
		}

		// Use a short read deadline so we can check for resize signals.
		stdin.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, err := stdin.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			if os.IsTimeout(err) {
				continue
			}
			return
		}
	}
}

func sendResize(dst io.Writer, fd int) error {
	cols, rows, err := term.GetSize(fd)
	if err != nil {
		return err
	}
	var msg [8]byte
	copy(msg[:4], resizeMagic[:])
	binary.BigEndian.PutUint16(msg[4:6], uint16(cols))
	binary.BigEndian.PutUint16(msg[6:8], uint16(rows))
	_, err = dst.Write(msg[:])
	return err
}

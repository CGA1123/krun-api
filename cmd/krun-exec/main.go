package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
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

	// 3. Send the username as the first line.
	if _, err := fmt.Fprintf(conn, "%s\n", user); err != nil {
		return fmt.Errorf("send user: %w", err)
	}

	// 4. Put local terminal into raw mode.
	stdinFd := int(os.Stdin.Fd())
	if !term.IsTerminal(stdinFd) {
		return fmt.Errorf("stdin is not a terminal")
	}

	oldState, err := term.MakeRaw(stdinFd)
	if err != nil {
		return fmt.Errorf("make raw: %w", err)
	}
	defer term.Restore(stdinFd, oldState)

	// 5. Bidirectional copy: local stdin/stdout <-> vsock connection.
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(conn, os.Stdin)
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

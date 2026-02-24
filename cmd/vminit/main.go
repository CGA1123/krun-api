//go:build unix

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"github.com/creack/pty"

	vminitv1 "github.com/CGA1123/krun-api/gen/vminit/v1"
	"github.com/CGA1123/krun-api/gen/vminit/v1/vminitv1connect"
	"golang.org/x/sys/unix"
)

const (
	vsockPortRPC  = 10000
	vsockPortExec = 10001
)

func main() {
	if os.Getpid() != 1 {
		log.Fatal("vminit: must run as PID 1 (set KRUN_INIT_PID1=1)")
	}

	if len(os.Args) < 2 {
		log.Fatal("vminit: no command specified")
	}

	log.Println("vminit: starting")

	// With KRUN_INIT_PID1=1, libkrun's built-in init is bypassed so we
	// must set up the guest environment ourselves.
	setupGuest()

	// Bind vsock listeners before spawning the child so no
	// guest process can race to claim the ports.
	shutdownCh := make(chan struct{})
	serverErrCh := make(chan error, 1)
	rpcListener := listenVsock(vsockPortRPC)
	execListener := listenVsock(vsockPortExec)

	go serveRPC(rpcListener, shutdownCh, serverErrCh)
	go serveExec(execListener)

	// Spawn the user command as a child process.
	cmd := exec.Command(os.Args[1], os.Args[2:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		log.Fatalf("vminit: failed to start command: %v", err)
	}

	childPid := cmd.Process.Pid
	log.Printf("vminit: started child process (pid %d): %v", childPid, os.Args[1:])

	// Forward signals to the child process.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGUSR1, syscall.SIGUSR2)
	go func() {
		for sig := range sigCh {
			_ = cmd.Process.Signal(sig)
		}
	}()

	// Reap zombie processes in a goroutine.
	go reapZombies()

	// Wait for either child exit or vsock shutdown.
	childDone := make(chan error, 1)
	go func() {
		childDone <- cmd.Wait()
	}()

	select {
	case err := <-childDone:
		if err != nil {
			log.Printf("vminit: child exited: %v", err)
		} else {
			log.Println("vminit: child exited successfully")
		}
	case <-shutdownCh:
		log.Println("vminit: shutdown requested, sending SIGTERM to child")
		_ = cmd.Process.Signal(syscall.SIGTERM)

		select {
		case <-childDone:
			log.Println("vminit: child exited after SIGTERM")
		case <-time.After(5 * time.Second):
			log.Println("vminit: child did not exit in time, proceeding with shutdown")
			_ = cmd.Process.Signal(syscall.SIGKILL)
		}
	case err := <-serverErrCh:
		log.Printf("vminit: RPC server failed: %v, shutting down", err)
		_ = cmd.Process.Signal(syscall.SIGKILL)
	}

	poweroff()
}

// --- guest setup ---

// setupGuest performs the guest environment setup that libkrun's built-in init
// normally handles. With KRUN_INIT_PID1=1, that init execs us directly, so we
// must do it ourselves: mount filesystems, bring up networking, set hostname.
func setupGuest() {
	// Core filesystem mounts.
	mount("proc", "/proc", "proc", 0)
	mount("sysfs", "/sys", "sysfs", 0)
	mount("devtmpfs", "/dev", "devtmpfs", 0)
	mount("devpts", "/dev/pts", "devpts", 0)
	mount("tmpfs", "/dev/shm", "tmpfs", 0)
	mount("cgroup2", "/sys/fs/cgroup", "cgroup2", 0)

	// Set hostname from KRUN_HOSTNAME env, default to "minion".
	hostname := os.Getenv("KRUN_HOSTNAME")
	if hostname == "" {
		hostname = "minion"
	}
	syscall.Sethostname([]byte(hostname))

	// Bring up loopback.
	run("ip", "link", "set", "lo", "up")

	// Bring up eth0 and get a DHCP lease.
	run("ip", "link", "set", "eth0", "up")
	run("udhcpc", "-i", "eth0", "-n", "-q")

	log.Println("vminit: guest setup complete")
}

func mount(fstype, target, source string, flags uintptr) {
	os.MkdirAll(target, 0o755)
	if err := syscall.Mount(source, target, fstype, flags, ""); err != nil {
		// Not fatal — may already be mounted.
		log.Printf("vminit: mount %s on %s: %v", fstype, target, err)
	}
}

func run(name string, args ...string) {
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		log.Printf("vminit: %s %v: %v: %s", name, args, err, out)
	}
}

// --- vsock helpers ---

// vsockListener wraps a raw AF_VSOCK file descriptor as a net.Listener.
// Go's net.FileListener and net.FileConn both call getsockname which
// AF_VSOCK doesn't support, so we use raw fds throughout.
type vsockListener struct {
	fd   int
	port uint32
}

func (l *vsockListener) Accept() (net.Conn, error) {
	nfd, _, err := unix.Accept(l.fd)
	if err != nil {
		return nil, err
	}
	return newVsockConn(nfd, l.port), nil
}

func (l *vsockListener) Close() error { return unix.Close(l.fd) }
func (l *vsockListener) Addr() net.Addr { return vsockAddr(l.port) }

type vsockConn struct {
	file *os.File
	port uint32
}

func newVsockConn(fd int, port uint32) *vsockConn {
	f := os.NewFile(uintptr(fd), fmt.Sprintf("vsock-%d", port))
	return &vsockConn{file: f, port: port}
}

func (c *vsockConn) Read(b []byte) (int, error)               { return c.file.Read(b) }
func (c *vsockConn) Write(b []byte) (int, error)              { return c.file.Write(b) }
func (c *vsockConn) Close() error                             { return c.file.Close() }
func (c *vsockConn) LocalAddr() net.Addr                      { return vsockAddr(c.port) }
func (c *vsockConn) RemoteAddr() net.Addr                     { return vsockAddr(c.port) }
func (c *vsockConn) SetDeadline(t time.Time) error            { return c.file.SetDeadline(t) }
func (c *vsockConn) SetReadDeadline(t time.Time) error        { return c.file.SetReadDeadline(t) }
func (c *vsockConn) SetWriteDeadline(t time.Time) error       { return c.file.SetWriteDeadline(t) }

type vsockAddr uint32

func (a vsockAddr) Network() string { return "vsock" }
func (a vsockAddr) String() string  { return fmt.Sprintf("vsock://*:%d", uint32(a)) }

func listenVsock(port uint32) net.Listener {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		log.Fatalf("vminit: socket: %v", err)
	}

	sa := &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}
	if err := unix.Bind(fd, sa); err != nil {
		log.Fatalf("vminit: bind vsock port %d: %v", port, err)
	}
	if err := unix.Listen(fd, 1); err != nil {
		log.Fatalf("vminit: listen vsock port %d: %v", port, err)
	}

	log.Printf("vminit: listening on vsock port %d", port)
	return &vsockListener{fd: fd, port: port}
}

// --- RPC server (shutdown) ---

func serveRPC(listener net.Listener, shutdownCh chan struct{}, serverErrCh chan<- error) {
	svc := &vmInitService{shutdownCh: shutdownCh}
	path, handler := vminitv1connect.NewVMInitServiceHandler(svc)

	mux := http.NewServeMux()
	mux.Handle(path, handler)

	server := &http.Server{Handler: mux}

	log.Println("vminit: serving Connect RPC on vsock")
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		serverErrCh <- err
	}
}

type vmInitService struct {
	vminitv1connect.UnimplementedVMInitServiceHandler
	shutdownCh chan struct{}
}

func (s *vmInitService) Shutdown(ctx context.Context, req *connect.Request[vminitv1.ShutdownRequest]) (*connect.Response[vminitv1.ShutdownResponse], error) {
	log.Println("vminit: Shutdown RPC received")

	unix.Sync()
	log.Println("vminit: sync() done")

	close(s.shutdownCh)

	return connect.NewResponse(&vminitv1.ShutdownResponse{}), nil
}

// --- exec server ---

// serveExec accepts connections on the exec vsock port. Each connection
// reads the username from the first line, spawns `su -l <user>` with a
// PTY, and pipes the connection to the PTY.
func serveExec(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("vminit: exec accept: %v", err)
			return
		}
		go handleExecSession(conn)
	}
}

func handleExecSession(conn net.Conn) {
	defer conn.Close()

	// Read the username from the first line.
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		log.Printf("vminit: exec read user: %v", err)
		return
	}
	user := strings.TrimSpace(line)
	if user == "" {
		user = "root"
	}

	log.Printf("vminit: exec session starting: user=%s", user)

	// Spawn a login shell with a PTY.
	// Use -s to specify the shell explicitly, avoiding su calling login(1)
	// which conflicts with the PTY setup from pty.Start.
	cmd := exec.Command("su", "-s", "/bin/sh", "-", user)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		log.Printf("vminit: exec start shell: %v", err)
		return
	}
	defer ptmx.Close()

	// If the reader buffered any extra bytes after the newline, write
	// them to the PTY so they aren't lost.
	if reader.Buffered() > 0 {
		buffered := make([]byte, reader.Buffered())
		n, _ := reader.Read(buffered)
		if n > 0 {
			ptmx.Write(buffered[:n])
		}
	}

	// Bidirectional copy between connection and PTY.
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(ptmx, conn)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, ptmx)
		done <- struct{}{}
	}()
	<-done

	conn.Close()
	ptmx.Close()
	<-done

	_ = cmd.Wait()
	log.Printf("vminit: exec session ended: user=%s", user)
}

// --- zombie reaper ---

func reapZombies() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGCHLD)

	for range sigCh {
		for {
			var status syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
			if pid <= 0 || err != nil {
				break
			}
		}
	}
}

// --- poweroff ---

func poweroff() {
	log.Println("vminit: syncing filesystems")
	unix.Sync()
	log.Println("vminit: calling reboot(POWER_OFF)")
	unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
}

//go:build unix

package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"connectrpc.com/connect"
	vminitv1 "github.com/CGA1123/krun-api/gen/vminit/v1"
	"github.com/CGA1123/krun-api/gen/vminit/v1/vminitv1connect"
	"golang.org/x/sys/unix"
)

const vsockPort = 10000

func main() {
	if os.Getpid() != 1 {
		log.Fatal("vminit: must run as PID 1")
	}

	if len(os.Args) < 2 {
		log.Fatal("vminit: no command specified")
	}

	log.Println("vminit: starting")

	// Bind the vsock listener before spawning the child so no
	// guest process can race to claim the port.
	shutdownCh := make(chan struct{})
	serverErrCh := make(chan error, 1)
	listener := bindVsock()
	go serveVsock(listener, shutdownCh, serverErrCh)

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

		// Wait briefly for the child to exit.
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

// bindVsock creates, binds, and listens on the vsock port, returning a
// net.Listener. Called synchronously before spawning the child process
// to prevent any guest process from racing to claim the port.
func bindVsock() net.Listener {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		log.Fatalf("vminit: socket: %v", err)
	}

	sa := &unix.SockaddrVM{
		CID:  unix.VMADDR_CID_ANY,
		Port: vsockPort,
	}

	if err := unix.Bind(fd, sa); err != nil {
		log.Fatalf("vminit: bind: %v", err)
	}

	if err := unix.Listen(fd, 1); err != nil {
		log.Fatalf("vminit: listen: %v", err)
	}

	log.Printf("vminit: listening on vsock port %d", vsockPort)

	f := os.NewFile(uintptr(fd), "vsock-listener")
	listener, err := net.FileListener(f)
	f.Close()
	if err != nil {
		log.Fatalf("vminit: FileListener: %v", err)
	}

	return listener
}

// serveVsock runs a Connect RPC server on the vsock listener.
// If the server exits unexpectedly, the error is sent on serverErrCh
// so the main goroutine can trigger a shutdown.
func serveVsock(listener net.Listener, shutdownCh chan struct{}, serverErrCh chan<- error) {
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

// vmInitService implements the VMInitService Connect RPC handler.
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

func poweroff() {
	log.Println("vminit: syncing filesystems")
	unix.Sync()
	log.Println("vminit: calling reboot(POWER_OFF)")
	unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
}

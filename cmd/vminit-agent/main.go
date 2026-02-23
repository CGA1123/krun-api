package main

import (
	"log"

	"golang.org/x/sys/unix"
)

const vsockPort = 10000

func main() {
	log.Println("vminit-agent: starting")

	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		log.Fatalf("vminit-agent: socket: %v", err)
	}

	sa := &unix.SockaddrVM{
		CID:  unix.VMADDR_CID_ANY,
		Port: vsockPort,
	}

	if err := unix.Bind(fd, sa); err != nil {
		log.Fatalf("vminit-agent: bind: %v", err)
	}

	if err := unix.Listen(fd, 1); err != nil {
		log.Fatalf("vminit-agent: listen: %v", err)
	}

	log.Printf("vminit-agent: listening on vsock port %d", vsockPort)

	// Wait for a connection — any connection triggers shutdown.
	nfd, _, err := unix.Accept(fd)
	if err != nil {
		log.Fatalf("vminit-agent: accept: %v", err)
	}

	log.Println("vminit-agent: connection received, syncing filesystems")
	unix.Sync()
	log.Println("vminit-agent: sync() done, signaling host")

	// Write "ok" back to confirm sync is done.
	unix.Write(nfd, []byte("ok"))
	unix.Close(nfd)

	// Now reboot with POWER_OFF — this is safe because the VMM runs in a
	// child process, so libkrun's _exit() only kills that child.
	log.Println("vminit-agent: calling reboot(POWER_OFF)")
	unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
}

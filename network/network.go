package network

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/containers/gvisor-tap-vsock/pkg/transport"
	"github.com/containers/gvisor-tap-vsock/pkg/types"
	"github.com/containers/gvisor-tap-vsock/pkg/virtualnetwork"
	"golang.org/x/net/proxy"
)

// VMNetwork manages the userspace virtual network for a single VM.
type VMNetwork struct {
	vn         *virtualnetwork.VirtualNetwork
	sockPath   string
	cancel     context.CancelFunc
	done       chan struct{}
	listenConn *net.UnixConn
}

// NewVMNetwork creates a virtual network for the given VM and starts listening
// on a unixgram socket. The caller should pass SocketPath() to libkrun's
// AddNetUnixgram with NET_FLAG_VFKIT set.
func NewVMNetwork(vmID string, opts Opts) (*VMNetwork, error) {
	cfg := &types.Configuration{
		MTU:               1500,
		Subnet:            "192.168.127.0/24",
		GatewayIP:         "192.168.127.1",
		GatewayMacAddress: "5a:94:ef:e4:0c:01",
		DHCPStaticLeases:  map[string]string{"192.168.127.2": opts.GuestMAC},
		Protocol:          types.VfkitProtocol,
	}

	dialer := buildDialer(opts)
	vn, err := virtualnetwork.New(cfg, dialer)
	if err != nil {
		return nil, fmt.Errorf("create virtual network: %w", err)
	}

	sockPath := filepath.Join(opts.SocketDir, vmID+".sock")

	// Remove stale socket if present.
	os.Remove(sockPath)

	// Listen on unixgram socket — same as gvproxy's --listen-vfkit mode.
	listeningConn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{
		Name: sockPath,
		Net:  "unixgram",
	})
	if err != nil {
		return nil, fmt.Errorf("listen unixgram %s: %w", sockPath, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)
		defer listeningConn.Close()
		defer os.Remove(sockPath)

		// Wait for the VM to connect and send the VFKT magic.
		// transport.AcceptVfkit peeks the first datagram to learn the remote address.
		vfkitConn, err := transport.AcceptVfkit(listeningConn)
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("accept vfkit failed", "vm", vmID, "error", err)
			}
			return
		}

		// Hand the connected datagram socket to gvisor-tap-vsock.
		if err := vn.AcceptVfkit(ctx, vfkitConn); err != nil {
			if ctx.Err() == nil {
				slog.Error("accept vfkit loop failed", "vm", vmID, "error", err)
			}
		}
	}()

	return &VMNetwork{
		vn:         vn,
		sockPath:   sockPath,
		cancel:     cancel,
		done:       done,
		listenConn: listeningConn,
	}, nil
}

// SocketPath returns the unix socket path to pass to libkrun.
func (n *VMNetwork) SocketPath() string {
	return n.sockPath
}

// Close shuts down the virtual network and cleans up the socket.
func (n *VMNetwork) Close() error {
	n.cancel()
	// Close the listening conn to unblock AcceptVfkit.
	n.listenConn.Close()
	select {
	case <-n.done:
	case <-time.After(3 * time.Second):
	}
	return nil
}

func buildDialer(opts Opts) func(network, addr string) (net.Conn, error) {
	if opts.ProxyAddr != "" {
		socksDialer, err := proxy.SOCKS5("tcp", opts.ProxyAddr, nil, proxy.Direct)
		if err != nil {
			slog.Error("failed to create SOCKS5 dialer", "proxy", opts.ProxyAddr, "error", err)
			return func(network, addr string) (net.Conn, error) {
				return nil, fmt.Errorf("SOCKS5 dialer init failed: %w", err)
			}
		}
		return func(network, addr string) (net.Conn, error) {
			slog.Info("guest connection via SOCKS5", "network", network, "addr", addr, "proxy", opts.ProxyAddr)
			return socksDialer.Dial(network, addr)
		}
	}

	return func(network, addr string) (net.Conn, error) {
		slog.Info("guest connection", "network", network, "addr", addr)
		return net.Dial(network, addr)
	}
}

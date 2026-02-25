package network

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/containers/gvisor-tap-vsock/pkg/transport"
	"github.com/containers/gvisor-tap-vsock/pkg/types"
	"github.com/containers/gvisor-tap-vsock/pkg/virtualnetwork"
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
	if opts.ProxyAddr != "" && len(opts.AllowList) > 0 {
		rules := parseAllowList(opts.AllowList)
		return func(network, addr string) (net.Conn, error) {
			if !matchesAllowList(addr, rules) {
				slog.Info("guest connection blocked", "network", network, "addr", addr)
				return nil, fmt.Errorf("blocked by allow list: %s", addr)
			}
			slog.Info("guest connection", "network", network, "addr", addr)
			return net.Dial(network, opts.ProxyAddr)
		}
	}

	if opts.ProxyAddr != "" {
		return func(network, addr string) (net.Conn, error) {
			slog.Info("guest connection", "network", network, "addr", addr)
			return net.Dial(network, opts.ProxyAddr)
		}
	}

	if len(opts.AllowList) > 0 {
		rules := parseAllowList(opts.AllowList)
		return func(network, addr string) (net.Conn, error) {
			if !matchesAllowList(addr, rules) {
				slog.Info("guest connection blocked", "network", network, "addr", addr)
				return nil, fmt.Errorf("blocked by allow list: %s", addr)
			}
			slog.Info("guest connection", "network", network, "addr", addr)
			return net.Dial(network, addr)
		}
	}

	return func(network, addr string) (net.Conn, error) {
		slog.Info("guest connection", "network", network, "addr", addr)
		return net.Dial(network, addr)
	}
}

type allowRule struct {
	network *net.IPNet
	port    int // 0 means all ports
}

func parseAllowList(rules []string) []allowRule {
	var parsed []allowRule
	for _, r := range rules {
		rule := allowRule{}
		parts := strings.SplitN(r, ":", 2)
		cidr := parts[0]
		if len(parts) == 2 {
			if p, err := strconv.Atoi(parts[1]); err == nil {
				rule.port = p
			}
		}
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			ip := net.ParseIP(cidr)
			if ip != nil {
				if ip.To4() != nil {
					_, network, _ = net.ParseCIDR(cidr + "/32")
				} else {
					_, network, _ = net.ParseCIDR(cidr + "/128")
				}
			}
			if network == nil {
				slog.Warn("skipping invalid allow rule", "rule", r)
				continue
			}
		}
		rule.network = network
		parsed = append(parsed, rule)
	}
	return parsed
}

func matchesAllowList(addr string, rules []allowRule) bool {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	port, _ := strconv.Atoi(portStr)

	for _, rule := range rules {
		if rule.network.Contains(ip) {
			if rule.port == 0 || rule.port == port {
				return true
			}
		}
	}
	return false
}


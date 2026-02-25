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
	"github.com/inetaf/tcpproxy"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const linkLocalSubnet = "169.254.0.0/16"

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

	vn, err := virtualnetwork.New(cfg)
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

// customTCPForwarder creates a TCP forwarder that routes connections through
// a custom dialer for proxy support and allow-list enforcement.
func customTCPForwarder(s *stack.Stack, opts Opts) *tcp.Forwarder {
	dialer := buildDialer(opts)

	return tcp.NewForwarder(s, 0, 2048, func(r *tcp.ForwarderRequest) {
		localAddress := r.ID().LocalAddress
		localPort := r.ID().LocalPort

		if linkLocal().Contains(localAddress) {
			r.Complete(true)
			return
		}

		dest := net.JoinHostPort(localAddress.String(), fmt.Sprint(localPort))

		outbound, err := dialer(dest)
		if err != nil {
			slog.Debug("custom dialer blocked", "dest", dest, "error", err)
			r.Complete(true)
			return
		}

		var wq waiter.Queue
		ep, tcpErr := r.CreateEndpoint(&wq)
		r.Complete(false)
		if tcpErr != nil {
			outbound.Close()
			slog.Error("CreateEndpoint failed", "error", tcpErr)
			return
		}

		remote := tcpproxy.DialProxy{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return outbound, nil
			},
		}
		remote.HandleConn(gonet.NewTCPConn(&wq, ep))
	})
}

func buildDialer(opts Opts) func(addr string) (net.Conn, error) {
	if opts.ProxyAddr != "" && len(opts.AllowList) > 0 {
		rules := parseAllowList(opts.AllowList)
		return func(addr string) (net.Conn, error) {
			if !matchesAllowList(addr, rules) {
				return nil, fmt.Errorf("blocked by allow list: %s", addr)
			}
			return net.Dial("tcp", opts.ProxyAddr)
		}
	}

	if opts.ProxyAddr != "" {
		return func(addr string) (net.Conn, error) {
			return net.Dial("tcp", opts.ProxyAddr)
		}
	}

	if len(opts.AllowList) > 0 {
		rules := parseAllowList(opts.AllowList)
		return func(addr string) (net.Conn, error) {
			if !matchesAllowList(addr, rules) {
				return nil, fmt.Errorf("blocked by allow list: %s", addr)
			}
			return net.Dial("tcp", addr)
		}
	}

	return func(addr string) (net.Conn, error) {
		return net.Dial("tcp", addr)
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

func linkLocal() *tcpip.Subnet {
	_, parsedSubnet, _ := net.ParseCIDR(linkLocalSubnet)
	subnet, _ := tcpip.NewSubnet(tcpip.AddrFromSlice(parsedSubnet.IP), tcpip.MaskFromBytes(parsedSubnet.Mask))
	return &subnet
}

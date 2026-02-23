package network

// Opts configures the per-VM virtual network.
type Opts struct {
	// GuestMAC is the MAC address assigned to the guest NIC.
	GuestMAC string

	// SocketDir is where the unix socket for this VM will be created.
	SocketDir string

	// ProxyAddr, if set, routes ALL outbound TCP through this address (transparent proxy).
	ProxyAddr string

	// AllowList, if set, only allows outbound connections matching these rules.
	// Format: "CIDR:port" or "CIDR" (all ports). Everything else is blocked.
	AllowList []string
}

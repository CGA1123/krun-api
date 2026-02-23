package machine

import (
	"crypto/sha256"
	"fmt"
)

// MACFromID generates a deterministic, locally-administered unicast MAC address
// from a VM ID string.
func MACFromID(vmID string) []uint8 {
	h := sha256.Sum256([]byte(vmID))
	mac := []uint8{
		h[0] | 0x02, // set locally administered bit
		h[1],
		h[2],
		h[3],
		h[4],
		h[5],
	}
	mac[0] &^= 0x01 // clear multicast bit
	return mac
}

// FormatMAC returns a MAC address as a colon-separated hex string.
func FormatMAC(mac []uint8) string {
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", mac[0], mac[1], mac[2], mac[3], mac[4], mac[5])
}

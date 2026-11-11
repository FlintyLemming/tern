// Package sysinfo reports facts about the host and the image that the
// overview page shows: the TCP congestion control in effect and the
// versions of the bundled tools.
package sysinfo

import (
	"os"
	"path/filepath"
	"strings"
)

// BBR describes the kernel's TCP congestion control as seen from inside
// the container. BBR is a node-wide switch set at deployment (compose
// sysctls); tern only reports it.
type BBR struct {
	Available []string `json:"available"` // tcp_available_congestion_control
	Current   string   `json:"current"`   // tcp_congestion_control
	Supported bool     `json:"supported"` // Available contains "bbr"
	Active    bool     `json:"active"`    // Current is "bbr"
}

// ReadBBR reads the two sysctl files under <procRoot>/sys/net/ipv4 ("/proc"
// in production). Files that cannot be read leave the zero value: on such
// a system BBR is simply reported as unsupported.
func ReadBBR(procRoot string) BBR {
	read := func(name string) string {
		raw, err := os.ReadFile(filepath.Join(procRoot, "sys", "net", "ipv4", name))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(raw))
	}
	b := BBR{Available: strings.Fields(read("tcp_available_congestion_control")), Current: read("tcp_congestion_control")}
	if b.Available == nil {
		b.Available = []string{}
	}
	for _, a := range b.Available {
		b.Supported = b.Supported || a == "bbr"
	}
	b.Active = b.Current == "bbr"
	return b
}

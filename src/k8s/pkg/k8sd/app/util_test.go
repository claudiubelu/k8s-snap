package app

import (
	"net"
	"testing"

	. "github.com/onsi/gomega"
)

func TestGetAllIPsFromInterface(t *testing.T) {
	tests := []struct {
		name        string
		ipAddr      string
		expectError bool
	}{
		{
			name:   "lo ipv4",
			ipAddr: "127.0.0.1",
		},
		{
			name:   "lo ipv6",
			ipAddr: "::1",
		},
		{
			name:        "address not found",
			ipAddr:      "8.8.8.8",
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			ip := net.ParseIP(tc.ipAddr)

			ips, err := getIPv46Addresses(ip)
			if tc.expectError {
				g.Expect(err).To(HaveOccurred())
				g.Expect(ips).To(BeEmpty())
				return
			}

			g.Expect(err).To(Not(HaveOccurred()))
			g.Expect(ips).To(HaveLen(1))
			g.Expect(ips[0].String()).To(Equal(tc.ipAddr))
		})
	}
}

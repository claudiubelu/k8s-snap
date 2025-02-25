package app

import (
	"fmt"
	"net"
	"slices"
)

// getIPv46Addresses returns an IPv4 and IPv6 pair if possible, from the local network interface which has the
// given IP address.
// getIPv46Addresses will return an array containing only the given IP address if the pair could not be found.
// If the given IP address cannot be found locally, an error will be returned.
func getIPv46Addresses(ip net.IP) ([]net.IP, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("Failed to get local interfaces: %w", err)
	}

	isIP := func(ifaceIP net.IP) bool {
		return ip.Equal(ifaceIP)
	}

	var lastError error
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			// Continue, we may still find the right interface.
			// Return lastError if we don't find it.
			lastError = fmt.Errorf("Failed to get local interface addresses: %w", err)
			continue
		}

		ips, err := parseIPAddresses(addrs)
		if err != nil {
			// Continue, we may still find the right interface.
			// Return lastError if we don't find it.
			lastError = err
			continue
		}

		// Check if the given IP is in the list.
		if !slices.ContainsFunc(ips, isIP) {
			continue
		}

		// Return a [IPv4, IPv6] pair if possible.
		givenIPv6 := ip.To4() == nil
		for _, ifIP := range ips {
			isIPv6 := ifIP.To4() == nil
			if isIPv6 == givenIPv6 {
				// same type, ignore.
				continue
			}

			if ifIP.IsGlobalUnicast() {
				return []net.IP{ip, ifIP}, nil
			}
		}

		// Couldn't find pair. Return the given IP.
		return []net.IP{ip}, nil
	}

	if lastError != nil {
		return nil, lastError
	}

	return nil, fmt.Errorf("failed to find a local interface associated with the node IP address '%s'", ip.String())
}

func parseIPAddresses(addrs []net.Addr) ([]net.IP, error) {
	ips := []net.IP{}
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}

		if ip == nil {
			return nil, fmt.Errorf("failed to parse node IP address '%s'", addr.String())
		}
		ips = append(ips, ip)
	}

	return ips, nil
}

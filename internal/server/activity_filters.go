package server

import (
	"errors"
	"net/netip"
	"net/url"
	"strings"
)

func loginActivityFilters(q url.Values) (string, *bool, error) {
	for _, key := range []string{"ip", "result"} {
		if len(q[key]) > 1 {
			return "", nil, errors.New("ambiguous activity filter")
		}
	}
	ip := strings.TrimSpace(q.Get("ip"))
	if ip != "" {
		addr, err := netip.ParseAddr(ip)
		if err != nil || addr.Zone() != "" {
			return "", nil, errors.New("ip must be an IPv4 or IPv6 address")
		}
		ip = addr.Unmap().String()
	}
	var success *bool
	switch q.Get("result") {
	case "", "all":
	case "success":
		value := true
		success = &value
	case "failed":
		value := false
		success = &value
	default:
		return "", nil, errors.New("result must be all, success or failed")
	}
	return ip, success, nil
}

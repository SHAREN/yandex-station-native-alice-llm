package main

import (
	"net"
	"net/netip"
)

// Loopback addresses work for firmware and bridge running on the same speaker.
// Other deployments must specify an origin reachable by the speaker.
func musicBaseURL(cfg config) string {
	if cfg.AssistantTools.MusicBaseURL != "" {
		return cfg.AssistantTools.MusicBaseURL
	}
	if cfg.TLSCert != "" {
		return ""
	}
	host, _, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return ""
	}
	ip, err := netip.ParseAddr(host)
	if host == "localhost" || (err == nil && ip.IsLoopback()) {
		return "http://" + cfg.Listen
	}
	return ""
}

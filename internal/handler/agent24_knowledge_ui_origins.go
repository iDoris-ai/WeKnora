// Agent24 fork product patch A24-KB-38 (KB-C2-01c5, WeKnora#133, UI-BRIDGE-v1 U4.4).
//
// AGENT24_KNOWLEDGE_UI_ORIGINS lists the browser origins that may call the UI read routes
// cross-origin in integration mode. Unset means none: no response carries a CORS header and
// every preflight is 404, as in A24-KB-37. Standalone mode ignores the variable.

package handler

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// Agent24KnowledgeUIOriginsEnv names the comma-separated origin allowlist for the UI read routes.
const Agent24KnowledgeUIOriginsEnv = "AGENT24_KNOWLEDGE_UI_ORIGINS"

// Agent24KnowledgeUIOriginsFromEnv returns the configured origins, deduplicated, in the order given.
// Unset or whitespace-only returns nil. Each entry, trimmed, must be an origin exactly as a browser
// serializes it in the Origin header: https://host[:port], or http://127.0.0.1:port or
// http://[::1]:port. Requests are matched by byte equality, so a non-canonical entry is refused
// here instead of silently never matching.
func Agent24KnowledgeUIOriginsFromEnv() ([]string, error) {
	raw := strings.TrimSpace(os.Getenv(Agent24KnowledgeUIOriginsEnv))
	if raw == "" {
		return nil, nil
	}
	var out []string
	seen := map[string]struct{}{}
	for _, item := range strings.Split(raw, ",") {
		o := strings.TrimSpace(item)
		if err := agent24ValidUIOrigin(o); err != nil {
			return nil, fmt.Errorf("%s: entry %q %w", Agent24KnowledgeUIOriginsEnv, o, err)
		}
		if _, dup := seen[o]; dup {
			continue
		}
		seen[o] = struct{}{}
		out = append(out, o)
	}
	return out, nil
}

func agent24ValidUIOrigin(o string) error {
	scheme, hostport, ok := strings.Cut(o, "://")
	if !ok {
		return errors.New("is not scheme://host[:port]")
	}
	host, port, hasPort, err := agent24SplitOriginHostPort(hostport)
	if err != nil {
		return err
	}
	if hasPort {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return errors.New("has an invalid port")
		}
	}
	switch scheme {
	case "https":
		if port == "443" {
			return errors.New("must omit the default port 443")
		}
	case "http":
		if host != "127.0.0.1" && host != "::1" {
			return errors.New("uses http: only http://127.0.0.1:<port> and http://[::1]:<port> are allowed")
		}
		if !hasPort || port == "80" {
			return errors.New("must give a loopback port other than 80")
		}
	default:
		return errors.New("must use https, or http on a loopback address")
	}
	return nil
}

// agent24SplitOriginHostPort accepts a lowercase DNS name, a canonical IPv4 address, or a canonical
// bracketed IPv6 address, optionally followed by :port. Anything else, a path or "*" included, is refused.
func agent24SplitOriginHostPort(hp string) (host, port string, hasPort bool, err error) {
	rest := ""
	if strings.HasPrefix(hp, "[") {
		end := strings.IndexByte(hp, ']')
		if end < 0 {
			return "", "", false, errors.New("has an unterminated IPv6 address")
		}
		host, rest = hp[1:end], hp[end+1:]
		ip := net.ParseIP(host)
		if ip == nil || ip.To4() != nil || ip.String() != host {
			return "", "", false, errors.New("has an IPv6 address that is not in canonical form")
		}
	} else {
		host, rest = hp, ""
		if i := strings.IndexByte(hp, ':'); i >= 0 {
			host, rest = hp[:i], hp[i:]
		}
		if !agent24OriginDNSNameOK(host) {
			return "", "", false, errors.New("has a host that is not a lowercase DNS name or IPv4 address")
		}
		// A browser parses a host whose last label is a number as IPv4 (WHATWG "ends in a number"), octal and
		// hex included, so such a host must already be the dotted-decimal form it serializes to.
		if agent24OriginEndsInNumber(host) {
			if ip := net.ParseIP(host); ip == nil || ip.To4() == nil || ip.String() != host {
				return "", "", false, errors.New("has an IPv4 address that is not in canonical form")
			}
		}
	}
	if rest == "" {
		return host, "", false, nil
	}
	if !strings.HasPrefix(rest, ":") {
		return "", "", false, errors.New("has text after the host that is not :port")
	}
	return host, rest[1:], true, nil
}

func agent24OriginEndsInNumber(h string) bool {
	last := h[strings.LastIndexByte(h, '.')+1:]
	digits := "0123456789"
	if strings.HasPrefix(last, "0x") {
		last, digits = last[2:], "0123456789abcdef"
		if last == "" {
			return true
		}
	}
	return strings.Trim(last, digits) == ""
}

func agent24OriginDNSNameOK(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

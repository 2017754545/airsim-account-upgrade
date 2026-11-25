package main

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Only a modem-supplied numeric SIP subscriber is a phone-number projection.
// This is not generic SIP URI equivalence: a SIP username may look numeric.
// Never use it for dial input, SMS destinations, SIM identity or authentication.
// Preserve unknown addresses verbatim rather than inventing a telephone number.
func mu300CallerNumber(raw string) string {
	raw = strings.TrimSpace(raw)
	scheme, rest, ok := strings.Cut(raw, ":")
	if !ok || (!strings.EqualFold(scheme, "sip") && !strings.EqualFold(scheme, "sips")) {
		return raw
	}
	if len(raw) > 512 || strings.ContainsAny(raw, "\r\n\t ") || strings.Count(rest, "@") != 1 {
		return raw
	}
	subscriber, destination, _ := strings.Cut(rest, "@")
	// A password, phone-context, extension or ISDN subaddress can distinguish
	// subscribers. Do not discard those components or guess a country prefix.
	if strings.ContainsAny(subscriber, ":;?") {
		return raw
	}
	subscriber, err := url.PathUnescape(subscriber)
	if err != nil {
		return raw
	}
	number := mu300NumericCaller(subscriber)
	if number == "" {
		return raw
	}
	parts := strings.Split(destination, ";")
	seen := map[string]bool{}
	for _, part := range parts[1:] {
		key, value, ok := strings.Cut(strings.ToLower(part), "=")
		if !ok || seen[key] {
			return raw
		}
		seen[key] = true
		switch key {
		case "user":
			if value != "phone" {
				return raw
			}
		case "transport":
			if value != "udp" && value != "tcp" && value != "tls" {
				return raw
			}
		default:
			return raw
		}
	}
	// Parse the authority without ever resolving or connecting to its host.
	u, err := url.Parse("sip://" + parts[0])
	if err != nil || u.User != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(parts[0], "%?#/\\") {
		return raw
	}
	host := u.Hostname()
	if strings.HasPrefix(u.Host, "[") {
		if net.ParseIP(host) == nil || !strings.Contains(host, ":") {
			return raw
		}
	} else {
		if len(host) == 0 || len(host) > 253 {
			return raw
		}
		for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return raw
			}
			for _, c := range label {
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
					return raw
				}
			}
		}
	}
	if strings.HasSuffix(u.Host, ":") {
		return raw
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return raw
		}
	}
	return number
}

func mu300NumericCaller(raw string) string {
	var b strings.Builder
	for _, c := range raw {
		switch {
		case c >= '0' && c <= '9':
			b.WriteRune(c)
		case c == '+' && b.Len() == 0:
			b.WriteRune(c)
		case c == '-' || c == '.' || c == '(' || c == ')' || c == ' ': // visual separators
		default:
			return ""
		}
	}
	number := b.String()
	digits := strings.TrimPrefix(number, "+")
	if len(digits) == 0 || len(digits) > 20 {
		return ""
	}
	return number
}

func mu300CallerIdentity(raw string) string {
	value := mu300CallerNumber(raw)
	if number := mu300NumericCaller(value); number != "" {
		return "number:" + number
	}
	// normalizeDialNumber returns empty for every SIP URI. Treating all such
	// results as equal would also merge genuinely different callers.
	return "opaque:" + value
}

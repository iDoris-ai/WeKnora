package types

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNormalizeProcessingEndpoint(t *testing.T) {
	cases := []struct {
		name, raw, want string
		ok              bool
	}{
		{
			"lowercase and default https port", "HTTPS://API.Example.COM/v1/embeddings?q=1#f",
			"https://api.example.com:443", true,
		},
		{
			"explicit port and userinfo", "http://user:secret@Example.COM:8080/path",
			"http://example.com:8080", true,
		},
		{"default http port", "http://localhost", "http://localhost:80", true},
		{"non-ascii host refused", "http://\u0130.example", "", false},
		{"percent-encoded non-ascii host refused", "http://%C4%B0.example", "", false},
		{"ipv6 zone refused", "http://[fe80::1%25eth0]:11434", "", false},
		{"ipv6 keeps brackets", "https://[2001:DB8::1]/x", "https://[2001:db8::1]:443", true},
		{"ipv6 with port", "http://[::1]:11434", "http://[::1]:11434", true},
		{"missing scheme", "api.example.com:443", "", false},
		{"missing host", "https:///v1", "", false},
		{"empty", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeProcessingEndpoint(tc.raw)
			if tc.ok {
				if err != nil || got != tc.want {
					t.Fatalf("NormalizeProcessingEndpoint(%q) = %q, %v; want %q", tc.raw, got, err, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("NormalizeProcessingEndpoint(%q) = %q, want error", tc.raw, got)
			}
		})
	}
}

type checkAllowGuard struct{}

func (checkAllowGuard) Allow(context.Context, ProcessingDestination) error { return nil }

// The send-time check: no guard passes; a guard needs an approval, and the URL sent must normalize to it.
func TestCheckProcessingRequestURL(t *testing.T) {
	const sent = "https://API.example.com/v1/S3NT1NEL?token=S3NT1NEL"
	if err := CheckProcessingRequestURL(context.Background(), sent); err != nil {
		t.Fatalf("no guard: %v", err)
	}
	guarded := WithProcessingGuard(context.Background(), checkAllowGuard{})
	if err := CheckProcessingRequestURL(guarded, sent); !errors.Is(err, ErrProcessingDenied) {
		t.Fatalf("guard without an approval: %v", err)
	}
	approved := WithProcessingApprovedEndpoint(guarded, "https://api.example.com:443")
	if err := CheckProcessingRequestURL(approved, sent); err != nil {
		t.Fatalf("approved origin: %v", err)
	}
	for _, other := range []string{
		"https://allowed.example@api.example.org/v1", "http://api.example.com/v1", "https://api.example.com:8443/v1",
		"https://S3NT1NEL.example/v1?token=S3NT1NEL",
	} {
		err := CheckProcessingRequestURL(approved, other)
		if !errors.Is(err, ErrProcessingDenied) {
			t.Fatalf("%s: %v", other, err)
		}
		if strings.Contains(err.Error(), "S3NT1NEL") || strings.Contains(err.Error(), "example") {
			t.Fatalf("error quotes the URL: %s", err.Error())
		}
	}
}

package utils

import "testing"

func TestSanitizeForLogStripsDelC1AndLineSeparators(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "del", in: "a\u007fb", want: "ab"},
		{name: "c1 nel", in: "a\u0085b", want: "ab"},
		{name: "line separator", in: "a\u2028b", want: "ab"},
		{name: "paragraph separator", in: "a\u2029b", want: "ab"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeForLog(tc.in); got != tc.want {
				t.Fatalf("SanitizeForLog(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

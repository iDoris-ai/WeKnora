package utils

import "testing"

func TestRedactQueryValues(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "plain",
			raw:  "q=hello&sig=abc",
			want: "q=%2A%2A%2A&sig=%2A%2A%2A",
		},
		{
			name: "repeated names",
			raw:  "state=one&state=two&code=abc",
			want: "code=%2A%2A%2A&state=%2A%2A%2A",
		},
		{
			name: "percent encoded",
			raw:  "next=%2Fsettings&q=hello%20world",
			want: "next=%2A%2A%2A&q=%2A%2A%2A",
		},
		{
			name: "empty",
			raw:  "",
			want: "",
		},
		{
			name: "invalid",
			raw:  "a=%ZZ",
			want: "[invalid query omitted]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactQueryValues(tt.raw)
			if got != tt.want {
				t.Fatalf("RedactQueryValues(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestRedactURL(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"https://user:pw@host.test/v1/rerank?key=abc#frag", "https://host.test/v1/rerank?key=%2A%2A%2A"},
		{"https://host.test/v1", "https://host.test/v1"},
		{"http://[::1", "[invalid url omitted]"},
		{"u:S3NT1NEL@api.test/v1", "[invalid url omitted]"},
		{"api.test/v1?key=S3NT1NEL", "[invalid url omitted]"},
		{"sk-S3NT1NEL@api.test/v1", "[invalid url omitted]"},
	}
	for _, tt := range tests {
		if got := RedactURL(tt.raw); got != tt.want {
			t.Fatalf("RedactURL(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

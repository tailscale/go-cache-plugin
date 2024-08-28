package revproxy

import (
	"net/url"
	"testing"
)

func TestCheckTarget(t *testing.T) {
	s := &Server{
		Targets: []string{"foo.com", "*.bar.com"},
	}
	tests := []struct {
		input string
		want  bool
	}{
		{"", false},
		{"nonesuch.org", false},
		{"foo.com", true},
		{"other.foo.com", false},
		{"bar.com", true},
		{"other.bar.com", true},
		{"some.other.bar.com", true},
	}
	for _, tc := range tests {
		u := &url.URL{Host: "localhost", Path: tc.input}
		if got := s.checkTarget(u); got != tc.want {
			t.Errorf("Check %q: got %v, want %v", tc.input, got, tc.want)
		}
	}
}

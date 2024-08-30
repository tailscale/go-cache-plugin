package revproxy

import (
	"testing"
)

func TestCheckTarget(t *testing.T) {
	testTargets := []string{"foo.com", "*.bar.com"}
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
		if got := hostMatchesTarget(tc.input, testTargets); got != tc.want {
			t.Errorf("Check %q: got %v, want %v", tc.input, got, tc.want)
		}
	}
}

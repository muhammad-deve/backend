package service

import (
	"strings"
	"testing"
)

func TestDeviceFieldKeepsPlainValues(t *testing.T) {
	cases := map[string]string{
		"0.1.9":                 "0.1.9",
		" windows ":             "windows",
		"darwin":                "darwin",
		"keepalive_timeout":     "keepalive_timeout",
		"1.0.0+build.5":         "1.0.0+build.5",
		"<script>":              "",
		"a b":                   "",
		"x'; DROP TABLE tokens": "",
		strings.Repeat("a", 40): strings.Repeat("a", 32),
	}
	for in, want := range cases {
		if got := deviceField(in); got != want {
			t.Errorf("deviceField(%q) = %q, want %q", in, got, want)
		}
	}
}

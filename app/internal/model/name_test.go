package model

import (
	"errors"
	"strings"
	"testing"
)

func TestCleanName(t *testing.T) {
	tests := []struct {
		in, want string
		err      error
	}{
		{in: "  Ada Lovelace  ", want: "Ada Lovelace"},
		{in: "", want: ""},
		{in: "Отабек Ўлмасов", want: "Отабек Ўлмасов"},
		{in: "J. Smith", want: "J. Smith"},
		{in: "John Company", want: "John Company"},
		{in: strings.Repeat("a", 60), want: strings.Repeat("a", 60)},
		{in: strings.Repeat("a", 61), err: ErrNameTooLong},
		{in: "Visit https://evil.example", err: ErrNameInvalid},
		{in: "www.evil", err: ErrNameInvalid},
		{in: "HTTP claim", err: ErrNameInvalid},
		{in: "goport-help.com", err: ErrNameInvalid},
		{in: "line\nbreak", err: ErrNameInvalid},
		{in: "abc‮def", err: ErrNameInvalid},
	}
	for _, test := range tests {
		got, err := CleanName(test.in, MaxNameLength)
		if !errors.Is(err, test.err) {
			t.Errorf("CleanName(%q) error = %v, want %v", test.in, err, test.err)
			continue
		}
		if err == nil && got != test.want {
			t.Errorf("CleanName(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestSafeName(t *testing.T) {
	if got := SafeName(strings.Repeat("b", 70)); got != strings.Repeat("b", 60) {
		t.Fatalf("long name = %q", got)
	}
	if got := SafeName("click www.evil.example"); got != "" {
		t.Fatalf("link name = %q, want empty", got)
	}
	if got := SafeName(" Ada "); got != "Ada" {
		t.Fatalf("SafeName = %q", got)
	}
}

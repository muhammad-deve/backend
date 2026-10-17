package service

import "testing"

func TestContainsBlockedWord(t *testing.T) {
	words := []blockedWord{
		{word: "spam"},
		{word: "Casino"},
		{word: "ass", wholeWord: true},
	}
	cases := map[string]bool{
		"spam":         true,
		"my-spam-site": true,
		"spammer":      true,
		"sp4m":         true, // 4 reads as a
		"5pam-bot":     true, // 5 reads as s
		"s-p-a-m":      true, // hyphens are ignored
		"best-casino":  true, // case doesn't matter
		"c4s1no":       true,
		"ass":          true,
		"my-ass":       true,
		"a55":          true,
		"class":        false, // whole-word entries don't match inside words
		"pass-check":   false,
		"myapp":        false,
		"spark-map":    false,
	}
	for subdomain, want := range cases {
		if got := containsBlockedWord(subdomain, words); got != want {
			t.Errorf("containsBlockedWord(%q) = %v, want %v", subdomain, got, want)
		}
	}
}

func TestContainsBlockedWordWithEmptyList(t *testing.T) {
	if containsBlockedWord("anything", nil) {
		t.Fatal("an empty list blocks nothing")
	}
}

package service

import (
	"errors"
	"log"
	"strings"
)

// errBlockedSubdomain is what the CLI shows. It deliberately doesn't name the
// word. "subdomain cannot" is one of the phrases 0.1.8+ CLIs treat as final,
// so a reconnecting CLI stops instead of retrying a name that will never pass.
var errBlockedSubdomain = errors.New("this subdomain cannot be used because it contains a word that isn't allowed; please choose a different name")

type blockedWord struct {
	word      string
	wholeWord bool
}

// blockedWords reads the blocked_words table. Errors (for example before the
// migration has run) are logged and treated as an empty list, so a problem
// here never stops every custom subdomain.
func (t *tcpService) blockedWords() []blockedWord {
	if t.app == nil {
		return nil
	}
	records, err := t.app.FindAllRecords("blocked_words")
	if err != nil {
		log.Printf("could not read blocked_words: %v", err)
		return nil
	}
	words := make([]blockedWord, 0, len(records))
	for _, r := range records {
		if w := strings.TrimSpace(r.GetString("word")); w != "" {
			words = append(words, blockedWord{word: w, wholeWord: r.GetBool("whole_word")})
		}
	}
	return words
}

// spellings returns s lowercased with hyphens and other symbols removed and
// common digit swaps read as letters. "1" can stand for "i" or "l", so there
// are two spellings.
func spellings(s string) [2]string {
	var withI, withL strings.Builder
	for _, r := range strings.ToLower(s) {
		switch r {
		case '0':
			r = 'o'
		case '3':
			r = 'e'
		case '4', '@':
			r = 'a'
		case '5', '$':
			r = 's'
		case '7':
			r = 't'
		case '8':
			r = 'b'
		case '1':
			withI.WriteRune('i')
			withL.WriteRune('l')
			continue
		}
		if r >= 'a' && r <= 'z' {
			withI.WriteRune(r)
			withL.WriteRune(r)
		}
	}
	return [2]string{withI.String(), withL.String()}
}

// containsBlockedWord reports whether subdomain uses any of the words: inside
// the name, or for whole-word entries as the full name or one of its
// hyphen-separated parts.
func containsBlockedWord(subdomain string, words []blockedWord) bool {
	name := spellings(subdomain)
	parts := strings.Split(strings.ToLower(subdomain), "-")
	for _, w := range words {
		target := spellings(w.word)[0]
		if len(target) < 2 {
			continue
		}
		if w.wholeWord {
			for _, part := range append(parts, subdomain) {
				for _, spelled := range spellings(part) {
					if spelled == target {
						return true
					}
				}
			}
			continue
		}
		for _, spelled := range name {
			if strings.Contains(spelled, target) {
				return true
			}
		}
	}
	return false
}

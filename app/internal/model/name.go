package model

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxNameLength is the longest name accepted at sign-up and shown in emails.
	MaxNameLength = 60
	// MaxProfileNameLength fits the dashboard's first and last name fields
	// (60 characters each) joined by a space.
	MaxProfileNameLength = 2*MaxNameLength + 1
)

var (
	ErrNameTooLong = errors.New("Name must be 60 characters or fewer.")
	ErrNameInvalid = errors.New("Name can't contain links or special characters.")
)

// domainLike matches a word followed by a common top-level domain, such as
// "goport-help.com".
var domainLike = regexp.MustCompile(`(?i)[a-z0-9-]\.(com|net|org|io|uz|ru|me|co|app|dev|xyz|link|site|online|top|info|biz|shop|ly)\b`)

// CleanName trims a display name and checks it. Names go into GoPort emails,
// so control characters and anything that looks like a link are refused. An
// empty name is allowed.
func CleanName(name string, maxLength int) (string, error) {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > maxLength {
		return "", ErrNameTooLong
	}
	for _, r := range name {
		// Control and invisible formatting characters (newlines, bidi
		// overrides, zero-width spaces). The emoji joiner is fine.
		if unicode.IsControl(r) || (unicode.Is(unicode.Cf, r) && r != '‍') {
			return "", ErrNameInvalid
		}
	}
	lower := strings.ToLower(name)
	if strings.Contains(lower, "://") || strings.Contains(lower, "www.") || strings.Contains(lower, "http") || domainLike.MatchString(name) {
		return "", ErrNameInvalid
	}
	return name, nil
}

// SafeName returns a name fit for an email greeting: a long name is cut to
// MaxNameLength, and a name that fails CleanName becomes "".
func SafeName(name string) string {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > MaxNameLength {
		name = strings.TrimSpace(string([]rune(name)[:MaxNameLength]))
	}
	clean, err := CleanName(name, MaxNameLength)
	if err != nil {
		return ""
	}
	return clean
}

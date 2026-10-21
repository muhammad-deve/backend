package disposable

import "testing"

func TestEmail(t *testing.T) {
	for _, email := range []string{"a@mailinator.com", "A@YOPMAIL.COM", "x@inbox.mailinator.com", "b@10minutemail.com", "c@guerrillamail.com "} {
		if !Email(email) {
			t.Errorf("%q not recognised as disposable", email)
		}
	}
	for _, email := range []string{"a@gmail.com", "b@mail.ru", "c@yandex.ru", "d@outlook.com", "e@icloud.com", "f@proton.me", "g@yurtal.io", "not-an-email", ""} {
		if Email(email) {
			t.Errorf("%q wrongly treated as disposable", email)
		}
	}
}

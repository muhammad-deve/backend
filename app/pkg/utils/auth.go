package utils

import "regexp"

func IsValidUzbPhoneNumber(phone string) bool {
	uzbPhoneRegex := `^998(90|91|93|94|95|97|98|99|50|88)\d{7}$`
	reg := regexp.MustCompile(uzbPhoneRegex)
	return reg.MatchString(phone)
}

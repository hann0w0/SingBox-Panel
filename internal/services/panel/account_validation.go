package panel

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxUsernameBytes  = 191
	minPasswordBytes  = 8
	maxPasswordBytes  = 72 // bcrypt ignores/rejects input beyond this boundary
	maxLoginJSONBytes = 8 << 10
)

var (
	errUsernameRequired = errors.New("username cannot be empty")
	errUsernameTooLong  = errors.New("username must not exceed 191 bytes")
	errUsernameControl  = errors.New("username cannot contain control characters")
	errPasswordTooShort = errors.New("password must be at least 8 bytes")
	errPasswordTooLong  = errors.New("password must not exceed 72 bytes")
)

func normalizeUsername(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validateUsername(value string) (string, string, error) {
	display := strings.TrimSpace(value)
	if display == "" {
		return "", "", errUsernameRequired
	}
	if !utf8.ValidString(display) || len(display) > maxUsernameBytes {
		return "", "", errUsernameTooLong
	}
	for _, r := range display {
		if unicode.IsControl(r) {
			return "", "", errUsernameControl
		}
	}
	return display, normalizeUsername(display), nil
}

func validateNewPassword(value string) error {
	if len(value) < minPasswordBytes {
		return errPasswordTooShort
	}
	if len(value) > maxPasswordBytes {
		return errPasswordTooLong
	}
	return nil
}

func validateLoginPassword(value string) error {
	if len(value) > maxPasswordBytes {
		return errPasswordTooLong
	}
	return nil
}

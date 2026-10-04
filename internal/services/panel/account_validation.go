package panel

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxUsernameBytes = 191
	// No minimum length or complexity: this panel serves a handful of known
	// users and the administrator chooses their passwords. The only limit is
	// bcrypt's 72-byte input boundary.
	maxPasswordBytes  = 72
	maxLoginJSONBytes = 8 << 10
)

var (
	errUsernameRequired = errors.New("username cannot be empty")
	errUsernameTooLong  = errors.New("username must not exceed 191 bytes")
	errUsernameControl  = errors.New("username cannot contain control characters")
	errUsernameReserved = errors.New("username is reserved")
	errPasswordRequired = errors.New("password cannot be empty")
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
	if normalizeUsername(display) == lockoutProxyUserName {
		return "", "", errUsernameReserved
	}
	return display, normalizeUsername(display), nil
}

func validateNewPassword(value string) error {
	if value == "" {
		return errPasswordRequired
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

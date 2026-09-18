package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/crypto/bcrypt"
)

const (
	DefaultAdminUsername = "admin"
	DefaultAdminPassword = "admin123"
	bcryptCost           = 12
	maxPasswordBytes     = 72
)

func NormalizeUsername(value string) (string, error) {
	username := strings.ToLower(strings.TrimSpace(value))
	if len(username) < 3 || len(username) > 64 {
		return "", errors.New("username must be between 3 and 64 characters")
	}
	for _, character := range username {
		if unicode.IsLower(character) || unicode.IsDigit(character) || character == '.' || character == '_' || character == '-' {
			continue
		}
		return "", errors.New("username may contain lowercase letters, digits, '.', '_', and '-'")
	}
	return username, nil
}

func ValidatePassword(password string) error {
	if len(password) < 8 {
		return errors.New("password must contain at least 8 characters")
	}
	if len(password) > maxPasswordBytes {
		return fmt.Errorf("password must not exceed %d bytes", maxPasswordBytes)
	}
	return nil
}

func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

func CheckPassword(hash, password string) bool {
	if hash == "" || ValidatePassword(password) != nil {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func NewSessionToken() (string, []byte, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", nil, fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	return token, HashSessionToken(token), nil
}

func HashSessionToken(token string) []byte {
	digest := sha256.Sum256([]byte(token))
	return append([]byte(nil), digest[:]...)
}

package safe

import (
	"errors"
	"fmt"
	"strings"
)

var ErrInvalidData = errors.New("invalid response data")

// AccessError indicates that required response data is missing or malformed.
type AccessError struct {
	Op     string
	Path   string
	Reason string
}

func (e *AccessError) Error() string {
	parts := []string{"data access error"}
	if strings.TrimSpace(e.Op) != "" {
		parts = append(parts, "op="+e.Op)
	}
	if strings.TrimSpace(e.Path) != "" {
		parts = append(parts, "path="+e.Path)
	}
	if strings.TrimSpace(e.Reason) != "" {
		parts = append(parts, "reason="+e.Reason)
	}
	return strings.Join(parts, ", ")
}

func (e *AccessError) Unwrap() error {
	return ErrInvalidData
}

func newAccessError(op, path, reason string) *AccessError {
	return &AccessError{
		Op:     strings.TrimSpace(op),
		Path:   strings.TrimSpace(path),
		Reason: strings.TrimSpace(reason),
	}
}

// FirstRef returns a pointer to the first element, or a typed error when the slice is empty.
func FirstRef[T any](op, path string, values []T) (*T, error) {
	if len(values) == 0 {
		return nil, newAccessError(op, path, "empty slice")
	}
	return &values[0], nil
}

// FirstString returns the first non-empty string from the slice head.
func FirstString(op, path string, values []string) (string, error) {
	if len(values) == 0 {
		return "", newAccessError(op, path, "empty string slice")
	}
	value := strings.TrimSpace(values[0])
	if value == "" {
		return "", newAccessError(op, path, "first value is empty")
	}
	return value, nil
}

// RequireStringMinLen validates string length for safe slicing/indexing.
func RequireStringMinLen(op, path, value string, minLen int) (string, error) {
	if minLen <= 0 {
		return value, nil
	}
	if len(value) < minLen {
		return "", newAccessError(op, path, fmt.Sprintf("string length %d < %d", len(value), minLen))
	}
	return value, nil
}

// ReleaseYear extracts YYYY safely from releaseDate-like values.
func ReleaseYear(op, path, releaseDate string) (string, error) {
	raw, err := RequireStringMinLen(op, path, strings.TrimSpace(releaseDate), 4)
	if err != nil {
		return "", err
	}
	return raw[:4], nil
}

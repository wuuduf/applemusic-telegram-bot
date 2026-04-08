package safe

import (
	"errors"
	"testing"
)

func TestReleaseYearShortReturnsAccessError(t *testing.T) {
	_, err := ReleaseYear("safe.test", "releaseDate", "20")
	if err == nil {
		t.Fatalf("expected error")
	}
	var accessErr *AccessError
	if !errors.As(err, &accessErr) {
		t.Fatalf("expected AccessError, got %T", err)
	}
}

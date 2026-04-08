package runv2

import (
	"testing"

	"github.com/itouakirai/mp4ff/mp4"
)

func TestTransformInitInvalidInputReturnsError(t *testing.T) {
	t.Parallel()
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("TransformInit panicked on invalid input: %v", rec)
		}
	}()

	_, err := TransformInit(mp4.NewMP4Init())
	if err == nil {
		t.Fatalf("expected error for invalid init segment")
	}
}

func TestSanitizeInitMissingTrackReturnsError(t *testing.T) {
	t.Parallel()
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("sanitizeInit panicked on missing track: %v", rec)
		}
	}()

	err := sanitizeInit(&mp4.InitSegment{})
	if err == nil {
		t.Fatalf("expected error when init segment has no track")
	}
}

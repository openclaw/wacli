package wa

import (
	"strings"
	"testing"
)

func TestMediaTypeFromString(t *testing.T) {
	for _, tc := range []string{"image", "video", "audio", "document"} {
		if _, err := MediaTypeFromString(tc); err != nil {
			t.Fatalf("expected %s to be supported: %v", tc, err)
		}
	}
	if _, err := MediaTypeFromString("nope"); err == nil {
		t.Fatalf("expected error for unsupported type")
	}
}

func TestMediaDownloadLengthRejectsOversizedMedia(t *testing.T) {
	_, err := mediaDownloadLength(MaxMediaDownloadSize + 1)
	if err == nil || !strings.Contains(err.Error(), "media too large") {
		t.Fatalf("expected media too large error, got %v", err)
	}
}

func TestMediaDownloadLength(t *testing.T) {
	if got, err := mediaDownloadLength(0); err != nil || got != -1 {
		t.Fatalf("length(0) = %d, %v; want -1, nil", got, err)
	}
	if got, err := mediaDownloadLength(123); err != nil || got != 123 {
		t.Fatalf("length(123) = %d, %v; want 123, nil", got, err)
	}
}

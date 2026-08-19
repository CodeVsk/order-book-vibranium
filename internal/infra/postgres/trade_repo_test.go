package postgres

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestEncodeTradeCursor_RoundTrip(t *testing.T) {
	testID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	testTime := time.Date(2025, 8, 13, 15, 30, 45, 123456789, time.UTC)

	encoded := encodeTradeCursor(testTime, testID)
	decodedTime, decodedID, err := decodeTradeCursor(encoded)

	if err != nil {
		t.Fatalf("decodeTradeCursor failed: %v", err)
	}

	if !decodedTime.Equal(testTime) {
		t.Errorf("timestamp mismatch: want %v, got %v", testTime, decodedTime)
	}

	if decodedID != testID {
		t.Errorf("id mismatch: want %v, got %v", testID, decodedID)
	}
}

func TestDecodeTradeCursor_InvalidBase64(t *testing.T) {
	_, _, err := decodeTradeCursor("not-base64!")

	if err == nil {
		t.Fatal("decodeTradeCursor should have returned an error for invalid base64")
	}
}

func TestDecodeTradeCursor_MissingSeparator(t *testing.T) {
	// base64 encode a string without the "|" separator
	noSeparator := "dGltZXN0YW1wLXdpdGgtbm8tc2VwYXJhdG9y" // "timestamp-with-no-separator" base64-encoded

	_, _, err := decodeTradeCursor(noSeparator)

	if err == nil {
		t.Fatal("decodeTradeCursor should have returned an error for missing separator")
	}
}

func TestDecodeTradeCursor_InvalidUUID(t *testing.T) {
	// Create a cursor with valid timestamp but invalid UUID
	validTime := time.Date(2025, 8, 13, 15, 30, 45, 123456789, time.UTC)
	raw := validTime.Format(time.RFC3339Nano) + "|not-a-uuid"

	encoded := base64.URLEncoding.EncodeToString([]byte(raw))
	_, _, err := decodeTradeCursor(encoded)

	if err == nil {
		t.Fatal("decodeTradeCursor should have returned an error for invalid UUID")
	}
}

func TestDecodeTradeCursor_InvalidTimestamp(t *testing.T) {
	// Create a cursor with invalid timestamp and valid UUID
	testID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	raw := "not-a-timestamp|" + testID.String()

	encoded := base64.URLEncoding.EncodeToString([]byte(raw))
	_, _, err := decodeTradeCursor(encoded)

	if err == nil {
		t.Fatal("decodeTradeCursor should have returned an error for invalid timestamp")
	}
}

package tenant

import (
	"testing"
	"time"
)

// TestTenantCursorRoundTrip 游标编解码往返保真；非法输入安全拒绝。
func TestTenantCursorRoundTrip(t *testing.T) {
	created := time.Date(2026, 9, 27, 8, 30, 0, 0, time.UTC)
	id := "11111111-1111-1111-1111-111111111111"
	enc := encodeTenantCursor(created, id)
	dec, decID, ok := decodeTenantCursor(enc)
	if !ok || !dec.Equal(created) || decID != id {
		t.Fatalf("round trip: enc=%q dec=%v id=%q ok=%v", enc, dec, decID, ok)
	}
	if _, _, ok := decodeTenantCursor("not-base64!!!"); ok {
		t.Fatal("invalid cursor should decode fail")
	}
	if _, _, ok := decodeTenantCursor("v1|2026-01-01T00:00:00Z|not-a-uuid"); ok {
		t.Fatal("invalid uuid in cursor should decode fail")
	}
	if _, _, ok := decodeTenantCursor("v2|2026-01-01T00:00:00Z|11111111-1111-1111-1111-111111111111"); ok {
		t.Fatal("unknown version should decode fail")
	}
}

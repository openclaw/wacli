package app

import "testing"

func TestSessionRevokedMarkerLifecycle(t *testing.T) {
	storeDir := t.TempDir()

	revoked, err := SessionRevoked(storeDir)
	if err != nil || revoked {
		t.Fatalf("initial SessionRevoked() = %v, %v; want false, nil", revoked, err)
	}
	if err := MarkSessionRevoked(storeDir, "logged_out"); err != nil {
		t.Fatalf("MarkSessionRevoked: %v", err)
	}
	revoked, err = SessionRevoked(storeDir)
	if err != nil || !revoked {
		t.Fatalf("marked SessionRevoked() = %v, %v; want true, nil", revoked, err)
	}
	if err := ClearSessionRevoked(storeDir); err != nil {
		t.Fatalf("ClearSessionRevoked: %v", err)
	}
	revoked, err = SessionRevoked(storeDir)
	if err != nil || revoked {
		t.Fatalf("cleared SessionRevoked() = %v, %v; want false, nil", revoked, err)
	}
}

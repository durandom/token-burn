package upgrade

import "testing"

func TestAssetName(t *testing.T) {
	got, err := assetName("v0.1.0", "darwin", "arm64")
	if err != nil {
		t.Fatalf("assetName() error = %v", err)
	}
	want := "token-burn_v0.1.0_darwin_arm64.tar.gz"
	if got != want {
		t.Fatalf("assetName() = %q, want %q", got, want)
	}
}

func TestAssetNameRejectsUnsupportedPlatform(t *testing.T) {
	if _, err := assetName("v0.1.0", "windows", "amd64"); err == nil {
		t.Fatal("assetName() error = nil, want error")
	}
	if _, err := assetName("v0.1.0", "linux", "386"); err == nil {
		t.Fatal("assetName() error = nil, want error")
	}
}

func TestNormalizeVersion(t *testing.T) {
	tests := map[string]string{
		"v0.1.0": "0.1.0",
		"0.1.0":  "0.1.0",
		"dev":    "dev",
	}
	for raw, want := range tests {
		if got := normalizeVersion(raw); got != want {
			t.Fatalf("normalizeVersion(%q) = %q, want %q", raw, got, want)
		}
	}
}

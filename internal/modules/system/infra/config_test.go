package infra

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	for _, tc := range []struct {
		name      string
		json      string
		wantError string
	}{
		{"valid", `{"data_dir":"sessions","listen_addr":"127.0.0.1:9000"}`, ""},
		{"malformed", `{"data_dir":`, "parse config"},
		{"unknown", `{"unknown":true}`, "unknown field"},
		{"trailing", `{} {}`, "trailing JSON value"},
		{"public address", `{"listen_addr":"0.0.0.0:8080"}`, "loopback host"},
		{"invalid port", `{"listen_addr":"127.0.0.1:abc"}`, "numeric port"},
		{"oversized", strings.Repeat(" ", maxConfigBytes+1), "exceeds 64 KiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tc.json), 0o600); err != nil {
				t.Fatal(err)
			}
			config, err := LoadConfig(path)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error = %v, want %q", err, tc.wantError)
				}
				return
			}
			if err != nil || config.DataDir != "sessions" {
				t.Fatalf("config = %+v, error = %v", config, err)
			}
		})
	}
}

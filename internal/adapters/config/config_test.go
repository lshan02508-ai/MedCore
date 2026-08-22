package config

import "testing"

func TestValidateDefaults(t *testing.T) {
	runtime, err := (Config{Backend: BackendConfig{BaseURL: "http://localhost:8000/"}}).Validate()
	if err != nil { t.Fatal(err) }
	if runtime.Server.Listen != ":8080" { t.Fatalf("listen = %q", runtime.Server.Listen) }
	if runtime.Backend.BaseURL != "http://localhost:8000" { t.Fatalf("base URL = %q", runtime.Backend.BaseURL) }
	if runtime.RequestTimeout <= 0 || runtime.ResponseHeaderTimeout <= 0 { t.Fatal("durations were not parsed") }
}

func TestValidateRejectsInvalidConfig(t *testing.T) {
	tests := []Config{
		{},
		{Backend: BackendConfig{BaseURL: "http://backend", ResponseHeaderTimeout: "invalid"}},
		{Server: ServerConfig{MaxRequestBodyBytes: -1}, Backend: BackendConfig{BaseURL: "http://backend"}},
	}
	for i, cfg := range tests {
		if _, err := cfg.Validate(); err == nil { t.Fatalf("case %d: expected an error", i) }
	}
}

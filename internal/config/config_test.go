package config

import (
	"log/slog"
	"net"
	"strings"
	"testing"
)

// TestLoad_TLSAndCORS_DefaultsEmpty verifies that when no TLS or CORS env vars
// are set, the corresponding ServerConfig fields default to empty string
// (which downstream code interprets as "feature disabled" — plain HTTP,
// wildcard CORS).
func TestLoad_TLSAndCORS_DefaultsEmpty(t *testing.T) {
	t.Setenv("TLS_CERT_FILE", "")
	t.Setenv("TLS_KEY_FILE", "")
	t.Setenv("TLS_CERT_PEM", "")
	t.Setenv("TLS_KEY_PEM", "")
	t.Setenv("CORS_ALLOWED_ORIGINS", "")

	cfg := Load()

	if cfg.Server.TLSCertFile != "" {
		t.Errorf("TLSCertFile: want empty, got %q", cfg.Server.TLSCertFile)
	}
	if cfg.Server.TLSKeyFile != "" {
		t.Errorf("TLSKeyFile: want empty, got %q", cfg.Server.TLSKeyFile)
	}
	if cfg.Server.TLSCertPEM != "" {
		t.Errorf("TLSCertPEM: want empty, got %q", cfg.Server.TLSCertPEM)
	}
	if cfg.Server.TLSKeyPEM != "" {
		t.Errorf("TLSKeyPEM: want empty, got %q", cfg.Server.TLSKeyPEM)
	}
	if cfg.Server.CORSAllowedOrigins != "" {
		t.Errorf("CORSAllowedOrigins: want empty, got %q", cfg.Server.CORSAllowedOrigins)
	}
}

func TestLoad_TLSAndCORS_FromEnv(t *testing.T) {
	t.Setenv("TLS_CERT_FILE", "/etc/tls/cert.pem")
	t.Setenv("TLS_KEY_FILE", "/etc/tls/key.pem")
	t.Setenv("TLS_CERT_PEM", "-----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----")
	t.Setenv("TLS_KEY_PEM", "-----BEGIN PRIVATE KEY-----\nMIIE...\n-----END PRIVATE KEY-----")
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://app.hospital.org,https://admin.hospital.org")

	cfg := Load()

	if cfg.Server.TLSCertFile != "/etc/tls/cert.pem" {
		t.Errorf("TLSCertFile: got %q", cfg.Server.TLSCertFile)
	}
	if cfg.Server.TLSKeyFile != "/etc/tls/key.pem" {
		t.Errorf("TLSKeyFile: got %q", cfg.Server.TLSKeyFile)
	}
	if cfg.Server.TLSCertPEM == "" || cfg.Server.TLSCertPEM[:5] != "-----" {
		t.Errorf("TLSCertPEM: got %q", cfg.Server.TLSCertPEM)
	}
	if cfg.Server.TLSKeyPEM == "" || cfg.Server.TLSKeyPEM[:5] != "-----" {
		t.Errorf("TLSKeyPEM: got %q", cfg.Server.TLSKeyPEM)
	}
	if cfg.Server.CORSAllowedOrigins != "https://app.hospital.org,https://admin.hospital.org" {
		t.Errorf("CORSAllowedOrigins: got %q", cfg.Server.CORSAllowedOrigins)
	}
}

func TestLoad_LogLevel_DefaultsInfo(t *testing.T) {
	t.Setenv("LOG_LEVEL", "")
	cfg := Load()
	if cfg.Server.LogLevel != "info" {
		t.Errorf("LogLevel default: got %q, want %q", cfg.Server.LogLevel, "info")
	}
}

func TestLoad_LogLevel_FromEnv(t *testing.T) {
	t.Setenv("LOG_LEVEL", "debug")
	cfg := Load()
	if cfg.Server.LogLevel != "debug" {
		t.Errorf("LogLevel: got %q, want %q", cfg.Server.LogLevel, "debug")
	}
}

func TestLoad_AppEnv_NormalisedLowercase(t *testing.T) {
	t.Setenv("APP_ENV", "Production")
	cfg := Load()
	if cfg.Server.AppEnv != "production" {
		t.Errorf("AppEnv: got %q, want %q", cfg.Server.AppEnv, "production")
	}
}

func TestLoad_AppEnv_DefaultsDevelopment(t *testing.T) {
	t.Setenv("APP_ENV", "")
	cfg := Load()
	if cfg.Server.AppEnv != "development" {
		t.Errorf("AppEnv default: got %q, want %q", cfg.Server.AppEnv, "development")
	}
}

func TestLoad_AppName_DefaultsFhirMap(t *testing.T) {
	t.Setenv("APP_NAME", "")
	cfg := Load()
	if cfg.Server.AppName != "fhir-map" {
		t.Errorf("AppName default: got %q, want %q", cfg.Server.AppName, "fhir-map")
	}
}

func TestLoad_AppName_FromEnv(t *testing.T) {
	t.Setenv("APP_NAME", "fhir-map-r4")
	cfg := Load()
	if cfg.Server.AppName != "fhir-map-r4" {
		t.Errorf("AppName: got %q, want %q", cfg.Server.AppName, "fhir-map-r4")
	}
}

func TestLoad_LogFormat_DefaultsJSON(t *testing.T) {
	t.Setenv("LOG_FORMAT", "")
	cfg := Load()
	if cfg.Server.LogFormat != "json" {
		t.Errorf("LogFormat default: got %q, want %q", cfg.Server.LogFormat, "json")
	}
}

func TestLoad_TrustedProxies_EmptyByDefault(t *testing.T) {
	t.Setenv("TRUSTED_PROXIES", "")
	cfg := Load()
	if cfg.Server.TrustedProxies != "" {
		t.Errorf("TrustedProxies default: got %q, want empty", cfg.Server.TrustedProxies)
	}
}

func TestParseLogLevel_ValidValues(t *testing.T) {
	cases := []struct {
		in   string
		want slog.Level
	}{
		{"", slog.LevelInfo},
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"WARN", slog.LevelWarn},
		{"error", slog.LevelError},
		{"  error  ", slog.LevelError},
	}
	for _, c := range cases {
		got, err := ParseLogLevel(c.in)
		if err != nil {
			t.Errorf("ParseLogLevel(%q): unexpected error %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ParseLogLevel(%q): got %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseLogLevel_InvalidValue(t *testing.T) {
	_, err := ParseLogLevel("verbose")
	if err == nil {
		t.Fatalf("ParseLogLevel(\"verbose\"): expected error, got nil")
	}
	if !strings.Contains(err.Error(), "LOG_LEVEL") {
		t.Errorf("error should name LOG_LEVEL; got %v", err)
	}
	for _, want := range []string{"debug", "info", "warn", "error"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should list accepted value %q; got %v", want, err)
		}
	}
}

func TestParseTrustedProxies_ValidCIDR(t *testing.T) {
	nets, err := ParseTrustedProxies("10.0.0.0/8, 172.16.0.0/12")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nets) != 2 {
		t.Fatalf("got %d nets, want 2", len(nets))
	}
	if !nets[0].Contains(net.ParseIP("10.1.2.3")) {
		t.Errorf("10.0.0.0/8 should contain 10.1.2.3")
	}
	if !nets[1].Contains(net.ParseIP("172.20.0.5")) {
		t.Errorf("172.16.0.0/12 should contain 172.20.0.5")
	}
}

func TestParseTrustedProxies_BarIP_TreatedAs32(t *testing.T) {
	nets, err := ParseTrustedProxies("192.168.1.10")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nets) != 1 {
		t.Fatalf("got %d nets, want 1", len(nets))
	}
	ones, bits := nets[0].Mask.Size()
	if ones != 32 || bits != 32 {
		t.Errorf("mask: got /%d (bits=%d), want /32 (bits=32)", ones, bits)
	}
	if !nets[0].Contains(net.ParseIP("192.168.1.10")) {
		t.Errorf("bare-IP entry should contain itself")
	}
	if nets[0].Contains(net.ParseIP("192.168.1.11")) {
		t.Errorf("/32 should NOT contain a different address")
	}
}

func TestParseTrustedProxies_InvalidEntry(t *testing.T) {
	_, err := ParseTrustedProxies("10.0.0.0/8, not-an-ip")
	if err == nil {
		t.Fatalf("expected error for invalid entry, got nil")
	}
	if !strings.Contains(err.Error(), "TRUSTED_PROXIES") {
		t.Errorf("error should name TRUSTED_PROXIES; got %v", err)
	}
	if !strings.Contains(err.Error(), "not-an-ip") {
		t.Errorf("error should name the invalid entry; got %v", err)
	}
}

func TestParseTrustedProxies_EmptyReturnsNil(t *testing.T) {
	nets, err := ParseTrustedProxies("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nets != nil {
		t.Errorf("empty input should return nil slice, got %v", nets)
	}
}

func TestParseTrustedProxies_WildcardCIDR_Rejected(t *testing.T) {
	for _, cidr := range []string{"0.0.0.0/0", "::/0", "10.0.0.1, 0.0.0.0/0"} {
		_, err := ParseTrustedProxies(cidr)
		if err == nil {
			t.Errorf("ParseTrustedProxies(%q): expected error for wildcard CIDR, got nil", cidr)
		}
		if err != nil && !strings.Contains(err.Error(), "wildcard") {
			t.Errorf("ParseTrustedProxies(%q): error should mention wildcard; got %v", cidr, err)
		}
	}
}

func TestLoad_LogLevel_UppercaseNormalised(t *testing.T) {
	t.Setenv("LOG_LEVEL", "DEBUG")
	cfg := Load()
	if cfg.Server.LogLevel != "debug" {
		t.Errorf("LogLevel uppercase not normalised: got %q, want %q", cfg.Server.LogLevel, "debug")
	}
}

func TestLoad_DatabaseURL_EmptyByDefault(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	cfg := Load()
	if cfg.Database.DatabaseURL != "" {
		t.Errorf("DatabaseURL default: got %q, want empty", cfg.Database.DatabaseURL)
	}
}

func TestLoad_DatabaseURL_FromEnv(t *testing.T) {
	const want = "postgres://u:p@host/db?sslmode=require"
	t.Setenv("DATABASE_URL", want)
	cfg := Load()
	if cfg.Database.DatabaseURL != want {
		t.Errorf("DatabaseURL: got %q, want %q", cfg.Database.DatabaseURL, want)
	}
}

func TestDSN_DatabaseURL_TakesPriority(t *testing.T) {
	const want = "postgres://u:p@dburl-host/db?sslmode=verify-full"
	t.Setenv("DATABASE_URL", want)
	t.Setenv("DB_HOST", "individual-host")
	cfg := Load()
	if got := cfg.Database.DSN(); got != want {
		t.Errorf("DSN() should return DATABASE_URL value: got %q, want %q", got, want)
	}
}

func TestDSN_FallbackToIndividualVars(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DB_HOST", "fallback-host")
	t.Setenv("DB_PORT", "5433")
	t.Setenv("DB_USER", "alice")
	t.Setenv("DB_PASSWORD", "secret")
	t.Setenv("DB_NAME", "mydb")
	t.Setenv("DB_SSL_MODE", "require")
	cfg := Load()
	const want = "postgres://alice:secret@fallback-host:5433/mydb?sslmode=require"
	if got := cfg.Database.DSN(); got != want {
		t.Errorf("DSN() fallback: got %q, want %q", got, want)
	}
}

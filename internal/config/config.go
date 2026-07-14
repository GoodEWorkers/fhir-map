// Package config provides application configuration from environment variables.
package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all application configuration.
type Config struct {
	Server   ServerConfig
	Database DatabaseConfig
}

// ServerConfig holds HTTP server configuration.
type ServerConfig struct {
	Port            int
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
	// TransformTimeout caps the wall-clock budget for a single StructureMap
	// $transform execution. The recursion cap bounds dependent-call depth, but
	// a crafted source resource can fan the cartesian source-product out by
	// data breadth within that cap; this deadline is the cooperative interrupt
	// that keeps one request from pinning a worker for the full WriteTimeout.
	// Override with SERVER_TRANSFORM_TIMEOUT (default 15s). Keep it below
	// WriteTimeout so the engine yields before the HTTP write deadline fires.
	TransformTimeout time.Duration
	// StrictTransform, when true, runs $transform in fail-loud strict mode: a
	// coercion failure or an unmapped translate code returns a 422
	// OperationOutcome instead of silently accepting/dropping, so an embedding
	// ETL pipeline can quarantine. Default false (lenient/best-effort).
	// Override with SERVER_TRANSFORM_STRICT=true.
	StrictTransform bool
	// TransformOutputValidation controls the $transform output-validation gate
	// (P0.2): "off" (default — output returned as-is, byte-identical), "lenient"
	// (validate and flag issues via a Warning header + log, still returning the
	// output), or "strict" (reject output that fails validation as a 422
	// OperationOutcome). Unrecognised values are treated as "off" with a startup
	// warning. Override with SERVER_TRANSFORM_VALIDATE_OUTPUT.
	TransformOutputValidation string
	// MaxBodyBytes caps every request body to prevent OOM/DoS.
	// Override with SERVER_MAX_BODY_BYTES (default 10 MiB).
	MaxBodyBytes int64
	// TLS configuration. Empty values mean "feature disabled" (server falls
	// back to plain HTTP with a startup warning). Inline PEM takes priority
	// over file paths when both are set — see cmd/server/main.go.
	TLSCertFile string // TLS_CERT_FILE
	TLSKeyFile  string // TLS_KEY_FILE
	TLSCertPEM  string // TLS_CERT_PEM (raw PEM)
	TLSKeyPEM   string // TLS_KEY_PEM  (raw PEM)
	// CORSAllowedOrigins is a comma-separated allow-list. Empty = wildcard
	// (`Access-Control-Allow-Origin: *`); non-empty restricts the response
	// to echoing matched origins only.
	CORSAllowedOrigins string // CORS_ALLOWED_ORIGINS
	LogLevel           string // LOG_LEVEL (debug|info|warn|error; default: info)
	AppEnv             string // APP_ENV (freeform; default: development; normalised to lowercase)
	AppName            string // APP_NAME (default: fhir-map)
	LogFormat          string // LOG_FORMAT (json|text; default: json)
	TrustedProxies     string // TRUSTED_PROXIES (comma-separated CIDRs/IPs; empty = direct)
}

// DatabaseConfig holds PostgreSQL connection configuration.
type DatabaseConfig struct {
	// DatabaseURL is the full PostgreSQL connection string (required — no default).
	// When set, it takes priority over individual DB_* variables in DSN().
	// Never log its value; log only presence via the startup confirmation.
	DatabaseURL     string // DATABASE_URL
	Host            string
	Port            int
	User            string
	Password        string
	Name            string
	SSLMode         string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

// DSN returns the PostgreSQL connection string.
func (d DatabaseConfig) DSN() string {
	if d.DatabaseURL != "" {
		return d.DatabaseURL
	}
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?sslmode=%s",
		d.User, d.Password, d.Host, d.Port, d.Name, d.SSLMode,
	)
}

// Load reads configuration from environment variables with sensible defaults.
func Load() Config {
	return Config{
		Server: ServerConfig{
			Port:                      getEnvInt("SERVER_PORT", 8080),
			ReadTimeout:               getEnvDuration("SERVER_READ_TIMEOUT", 30*time.Second),
			WriteTimeout:              getEnvDuration("SERVER_WRITE_TIMEOUT", 30*time.Second),
			IdleTimeout:               getEnvDuration("SERVER_IDLE_TIMEOUT", 120*time.Second),
			ShutdownTimeout:           getEnvDuration("SERVER_SHUTDOWN_TIMEOUT", 15*time.Second),
			TransformTimeout:          getEnvDuration("SERVER_TRANSFORM_TIMEOUT", 15*time.Second),
			StrictTransform:           getEnvBool("SERVER_TRANSFORM_STRICT", false),
			TransformOutputValidation: strings.ToLower(strings.TrimSpace(getEnv("SERVER_TRANSFORM_VALIDATE_OUTPUT", "off"))),
			MaxBodyBytes:              clampMaxBodyBytes(int64(getEnvInt("SERVER_MAX_BODY_BYTES", 10<<20))), // 10 MiB default; clamped to a 4 KiB floor
			TLSCertFile:               getEnv("TLS_CERT_FILE", ""),
			TLSKeyFile:                getEnv("TLS_KEY_FILE", ""),
			TLSCertPEM:                getEnv("TLS_CERT_PEM", ""),
			TLSKeyPEM:                 getEnv("TLS_KEY_PEM", ""),
			CORSAllowedOrigins:        getEnv("CORS_ALLOWED_ORIGINS", ""),
			LogLevel:                  strings.ToLower(strings.TrimSpace(getEnv("LOG_LEVEL", "info"))),
			AppEnv:                    strings.ToLower(strings.TrimSpace(getEnv("APP_ENV", "development"))),
			AppName:                   getEnv("APP_NAME", "fhir-map"),
			LogFormat:                 strings.ToLower(strings.TrimSpace(getEnv("LOG_FORMAT", "json"))),
			TrustedProxies:            getEnv("TRUSTED_PROXIES", ""),
		},
		Database: DatabaseConfig{
			DatabaseURL:     getEnv("DATABASE_URL", ""),
			Host:            getEnv("DB_HOST", "localhost"),
			Port:            getEnvInt("DB_PORT", 5432),
			User:            getEnv("DB_USER", "fhir"),
			Password:        getEnv("DB_PASSWORD", "fhir"),
			Name:            getEnv("DB_NAME", "fhir"),
			SSLMode:         getEnv("DB_SSL_MODE", "disable"),
			MaxConns:        getEnvInt32("DB_MAX_CONNS", 25),
			MinConns:        getEnvInt32("DB_MIN_CONNS", 5),
			MaxConnLifetime: getEnvDuration("DB_MAX_CONN_LIFETIME", 1*time.Hour),
			MaxConnIdleTime: getEnvDuration("DB_MAX_CONN_IDLE_TIME", 30*time.Minute),
		},
	}
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

// getEnvBool reads a boolean env var (strconv.ParseBool: 1/t/T/TRUE/true/...).
// An unset or unparseable value yields defaultValue.
func getEnvBool(key string, defaultValue bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return defaultValue
}

// getEnvInt32 reads an int env var as int32, falling back to the default when
// unset, unparseable, or outside the int32 range. ParseInt with bitSize=32
// bounds the result to int32, defending against integer overflow on
// operator-supplied pool sizes.
func getEnvInt32(key string, defaultValue int32) int32 {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.ParseInt(v, 10, 32); err == nil {
			return int32(i)
		}
	}
	return defaultValue
}

// clampMaxBodyBytes enforces a sane lower bound on the body-size limit.
// http.MaxBytesReader with n <= 0 fails every request at the first byte,
// effectively turning the server into a 100% 413 responder. A misconfigured
// SERVER_MAX_BODY_BYTES (negative, zero, or absurdly small) must not silently
// brick the API; clamp to a 4 KiB minimum that still accommodates the smallest
// real request bodies the server expects.
func clampMaxBodyBytes(n int64) int64 {
	const minBodyBytes int64 = 4 << 10 // 4 KiB
	if n < minBodyBytes {
		return minBodyBytes
	}
	return n
}

func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return defaultValue
}

// ParseLogLevel converts a LOG_LEVEL string to slog.Level.
// Accepts debug|info|warn|warning|error (case-insensitive). Empty returns
// slog.LevelInfo. On unrecognised input it returns slog.LevelInfo AND a
// descriptive error — the safe zero value is only acceptable because every
// production caller exits on error; other callers must not ignore the error.
func ParseLogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("LOG_LEVEL=%q invalid; accepted values: debug|info|warn|error", s)
	}
}

// ParseTrustedProxies parses a comma-separated TRUSTED_PROXIES value into a
// slice of *net.IPNet. Bare IPs (no `/mask`) are treated as `/32` for IPv4
// or `/128` for IPv6. Whitespace around entries is trimmed. An empty input
// returns (nil, nil) — caller treats nil as "direct mode" (no trusted proxy).
// An invalid entry returns a descriptive error naming the offending value.
func ParseTrustedProxies(s string) ([]net.IPNet, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]net.IPNet, 0, len(parts))
	for _, raw := range parts {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			_, network, err := net.ParseCIDR(entry)
			if err != nil {
				return nil, fmt.Errorf("TRUSTED_PROXIES contains invalid CIDR %q: %w", entry, err)
			}
			// Reject wildcard CIDRs (0.0.0.0/0, ::/0): trusting all peers
			// lets any client forge X-Forwarded-For, poisoning the audit log.
			if isWildcardMask(network.Mask) {
				return nil, fmt.Errorf("TRUSTED_PROXIES contains wildcard CIDR %q: trusting all peers enables X-Forwarded-For spoofing; specify a narrower subnet", entry)
			}
			out = append(out, *network)
			continue
		}
		ip := net.ParseIP(entry)
		if ip == nil {
			return nil, fmt.Errorf("TRUSTED_PROXIES contains invalid IP %q", entry)
		}
		bits := 32
		if ip.To4() == nil {
			bits = 128
		}
		mask := net.CIDRMask(bits, bits)
		// Normalise to canonical 4-byte form for IPv4 so Contains works correctly.
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
		out = append(out, net.IPNet{IP: ip, Mask: mask})
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// isWildcardMask reports whether every bit in the mask is zero (i.e. matches
// any address). Used to reject 0.0.0.0/0 and ::/0 in ParseTrustedProxies.
func isWildcardMask(m net.IPMask) bool {
	for _, b := range m {
		if b != 0 {
			return false
		}
	}
	return true
}

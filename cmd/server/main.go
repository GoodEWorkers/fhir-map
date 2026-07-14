// Package main is the entry point for the FHIR ConceptMap server.
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/goodeworkers/fhir-map/internal/config"
	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/internal/domain/structuredefinition"
	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
	"github.com/goodeworkers/fhir-map/internal/handler"
	"github.com/goodeworkers/fhir-map/internal/repository/postgres"
	"github.com/goodeworkers/fhir-map/internal/transform"
	"github.com/goodeworkers/fhir-map/internal/transform/resolver"
	"github.com/goodeworkers/fhir-map/internal/translate"
)

func main() {
	cfg := config.Load()

	logLevel, err := config.ParseLogLevel(cfg.Server.LogLevel)
	if err != nil {
		slog.Error("invalid LOG_LEVEL", "error", err)
		os.Exit(1)
	}
	if cfg.Server.LogFormat != "json" && cfg.Server.LogFormat != "text" {
		slog.Error("invalid LOG_FORMAT", "value", cfg.Server.LogFormat, "accepted", "json|text")
		os.Exit(1)
	}
	trustedProxies, err := config.ParseTrustedProxies(cfg.Server.TrustedProxies)
	if err != nil {
		slog.Error("invalid TRUSTED_PROXIES", "error", err)
		os.Exit(1)
	}

	// Structured logging with no PHI in output; every line carries service and env for observability-pipeline routing (Datadog/Splunk/Loki).
	var handlerSlog slog.Handler
	logOpts := &slog.HandlerOptions{Level: logLevel}
	if cfg.Server.LogFormat == "text" {
		handlerSlog = slog.NewTextHandler(os.Stdout, logOpts)
	} else {
		handlerSlog = slog.NewJSONHandler(os.Stdout, logOpts)
	}
	logger := slog.New(handlerSlog).With(
		"service", cfg.Server.AppName,
		"env", cfg.Server.AppEnv,
	)
	slog.SetDefault(logger)
	logger.Info("log level configured",
		"log_level", cfg.Server.LogLevel,
		"log_format", cfg.Server.LogFormat,
	)
	if cfg.Server.TrustedProxies == "" {
		logger.Warn("TRUSTED_PROXIES not set; client_ip will reflect the immediate TCP peer address — set this to your proxy/LB CIDR range for accurate client IP logging")
	}
	if cfg.Server.LogLevel == "debug" {
		logger.Warn("LOG_LEVEL=debug: request_uri including query strings will be logged — this level is not suitable for environments processing PHI")
	}
	// Hard stops: combinations that would silently violate HIPAA audit requirements.
	if cfg.Server.LogLevel == "debug" && cfg.Server.AppEnv == "production" {
		logger.Error("LOG_LEVEL=debug is not permitted in APP_ENV=production: request_uri (including PHI-adjacent query strings) would be written to logs")
		os.Exit(1)
	}
	if cfg.Server.LogFormat == "text" && cfg.Server.AppEnv == "production" {
		logger.Warn("LOG_FORMAT=text in production: JSON-consuming audit pipelines (Splunk/Datadog/Loki) will not parse structured fields — switch to LOG_FORMAT=json for HIPAA-compliant audit logging")
	}

	// Required-secret validation: fail loudly before any connection attempts.
	// Presence logged, value never logged — HIPAA audit requirement.
	if cfg.Database.DatabaseURL == "" {
		logger.Error("DATABASE_URL is required but not set; set to a postgres:// connection string")
		os.Exit(1)
	}
	// Parse sslmode from DATABASE_URL so the audit log reflects the actual TLS
	// policy, not the DB_SSL_MODE fallback field (which defaults to "disable").
	sslMode := cfg.Database.SSLMode
	if u, perr := url.Parse(cfg.Database.DatabaseURL); perr == nil {
		if v := u.Query().Get("sslmode"); v != "" {
			sslMode = v
		}
	}
	logger.Info("database configuration verified",
		"database_url_set", true,
		"db_ssl_mode", sslMode,
	)

	// Connect to PostgreSQL (DSN contains password — never logged).
	pool, err := connectDB(cfg.Database)
	if err != nil {
		logger.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}

	if cfg.Database.DatabaseURL != "" {
		logger.Info("connected to database", "database_url_set", true)
	} else {
		logger.Info("connected to database", "host", cfg.Database.Host, "port", cfg.Database.Port, "name", cfg.Database.Name)
	}

	// Run migrations
	if err := runMigrations(cfg.Database.DSN()); err != nil {
		pool.Close()
		logger.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}
	logger.Info("migrations complete")

	// Initialize layers. Production $translate uses FlatEngine (indexed lookups) for performance; JSONB path available for tests and inline fallback.
	repo := postgres.NewConceptMapRepo(pool)
	service := conceptmap.NewService(repo)
	mappingStore := postgres.NewMappingStore(pool)
	engine := translate.NewFlatEngine(mappingStore)

	scheme := "http"
	if cfg.Server.TLSCertPEM != "" || cfg.Server.TLSCertFile != "" {
		scheme = "https"
	}
	baseURL := fmt.Sprintf("%s://localhost:%d", scheme, cfg.Server.Port)
	cmHandlerR5 := handler.NewConceptMapHandler(service, engine, baseURL, logger).WithHistory(repo)
	cmHandlerR4 := handler.NewR4ConceptMapHandler(service, engine, baseURL, logger).WithHistory(repo)

	smRepo := postgres.NewStructureMapRepo(pool)
	smService := structuremap.NewService(smRepo)

	// StructureDefinition registry + type resolver.
	sdRepo := postgres.NewStructureDefinitionRepo(pool)
	sdService := structuredefinition.NewService(sdRepo)
	typeResolver := resolver.NewResolver(sdRepo)
	transformEng := transform.New(
		transform.WithTranslator(engine),
		transform.WithMapResolver(smService),
		transform.WithTypeResolver(typeResolver),
		transform.WithStrictTransform(cfg.Server.StrictTransform),
	)

	smHandlerR5 := handler.NewStructureMapHandler(smService, baseURL, logger).WithHistory(smRepo).WithTransformEngine(transformEng).WithTransformTimeout(cfg.Server.TransformTimeout)
	smHandlerR4 := handler.NewR4StructureMapHandler(smService, baseURL, logger).WithHistory(smRepo).WithTransformEngine(transformEng).WithTransformTimeout(cfg.Server.TransformTimeout)

	// Output-validation gate (opt-in via SERVER_TRANSFORM_VALIDATE_OUTPUT).
	switch cfg.Server.TransformOutputValidation {
	case "strict", "lenient":
		ov := handler.NewStructuralOutputValidator()
		strict := cfg.Server.TransformOutputValidation == "strict"
		smHandlerR5.WithTransformOutputValidation(ov, strict)
		smHandlerR4.WithTransformOutputValidation(ov, strict)
		logger.Info("transform output-validation gate enabled", "mode", cfg.Server.TransformOutputValidation)
	case "", "off":
		// disabled (default) — output returned byte-identical
	default:
		logger.Warn("unrecognised SERVER_TRANSFORM_VALIDATE_OUTPUT; output-validation gate left off",
			"value", cfg.Server.TransformOutputValidation)
	}

	sdHandlerR5 := handler.NewStructureDefinitionHandler(sdService, baseURL, logger).WithHistory(sdRepo)
	sdHandlerR4 := handler.NewR4StructureDefinitionHandler(sdService, baseURL, logger).WithHistory(sdRepo)

	mux := http.NewServeMux()
	cmHandlerR5.RegisterRoutes(mux)               // /fhir
	cmHandlerR5.RegisterRoutesAtPrefix(mux, "R5") // /fhir/R5
	cmHandlerR4.RegisterRoutes(mux)               // /fhir/R4 (handler's prefix)
	smHandlerR5.RegisterRoutes(mux)
	smHandlerR5.RegisterRoutesAtPrefix(mux, "R5")
	smHandlerR4.RegisterRoutes(mux)
	sdHandlerR5.RegisterRoutes(mux)
	sdHandlerR5.RegisterRoutesAtPrefix(mux, "R5")
	sdHandlerR4.RegisterRoutes(mux)

	// Health check endpoint
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"status":"healthy","timestamp":"%s"}`, time.Now().UTC().Format(time.RFC3339))
	})

	// Prometheus metrics endpoint.
	mux.Handle("GET /metrics", handler.MetricsHandler())

	// Apply middleware
	// [SYM-GR-0218] Request ID for audit trail correlation
	// [SYM-GR-0227] MaxBodyBytesMiddleware is OUTERMOST (first in the variadic
	// list — handler.Middleware wraps right-to-left, so the first argument
	// becomes the outermost wrap). Outermost is the correct position for the
	// body cap: it caps r.Body before any downstream middleware (logging,
	// CORS, request-id) gets a chance to mishandle it, and the cap is
	// effective for every endpoint registered on the mux.
	finalHandler := handler.Middleware(mux,
		handler.MaxBodyBytesMiddleware(cfg.Server.MaxBodyBytes),
		handler.SecurityHeadersMiddleware,
		handler.NewCORSMiddleware(cfg.Server.CORSAllowedOrigins),
		handler.RequestIDMiddleware,
		handler.LoggingMiddleware(logger, trustedProxies),
		handler.MetricsMiddleware,
	)

	// Configure HTTP server
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:      finalHandler,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	if cfg.Server.TLSKeyFile != "" && cfg.Server.TLSCertFile == "" && cfg.Server.TLSCertPEM == "" {
		logger.Warn("TLS_KEY_FILE is set but TLS_CERT_FILE and TLS_CERT_PEM are both empty; TLS is disabled")
	}
	if cfg.Server.TLSKeyPEM != "" && cfg.Server.TLSCertPEM == "" && cfg.Server.TLSCertFile == "" {
		logger.Warn("TLS_KEY_PEM is set but TLS_CERT_PEM and TLS_CERT_FILE are both empty; TLS is disabled")
	}

	// TLS startup: cert loading before serve goroutine so invalid cert exits with named error before port bind. Inline PEM takes priority over file paths.
	if cfg.Server.TLSCertPEM != "" || cfg.Server.TLSCertFile != "" {
		var (
			cert    tls.Certificate
			certErr error
			source  string
		)
		if cfg.Server.TLSCertPEM != "" {
			source = "TLS_CERT_PEM"
			if cfg.Server.TLSKeyPEM == "" {
				pool.Close()
				logger.Error("TLS_KEY_PEM is required when TLS_CERT_PEM is set")
				os.Exit(1)
			}
			cert, certErr = tls.X509KeyPair([]byte(cfg.Server.TLSCertPEM), []byte(cfg.Server.TLSKeyPEM))
		} else {
			source = "TLS_CERT_FILE"
			if cfg.Server.TLSKeyFile == "" {
				pool.Close()
				logger.Error("TLS_KEY_FILE is required when TLS_CERT_FILE is set")
				os.Exit(1)
			}
			cert, certErr = tls.LoadX509KeyPair(cfg.Server.TLSCertFile, cfg.Server.TLSKeyFile)
		}
		if certErr != nil {
			pool.Close()
			logger.Error("invalid TLS certificate", "var", source, "error", certErr)
			os.Exit(1)
		}
		srv.TLSConfig = &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		}
		logger.Info("TLS enabled", "source", source, "min_version", "TLS 1.2")
	}

	if srv.TLSConfig == nil {
		logger.Warn("TLS not configured; serving plain HTTP — not suitable for HIPAA production deployment")
	}

	// Graceful shutdown
	go func() {
		logger.Info("server starting", "port", cfg.Server.Port)
		var serveErr error
		if srv.TLSConfig != nil {
			serveErr = srv.ListenAndServeTLS("", "")
		} else {
			serveErr = srv.ListenAndServe()
		}
		if serveErr != nil && serveErr != http.ErrServerClosed {
			logger.Error("server failed", "error", serveErr)
			os.Exit(1)
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)

	if err := srv.Shutdown(ctx); err != nil {
		cancel()
		pool.Close()
		logger.Error("server forced to shutdown", "error", err)
		os.Exit(1)
	}
	cancel()

	pool.Close()
	logger.Info("server stopped")
}

func connectDB(cfg config.DatabaseConfig) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("failed to parse database config: %w", err)
	}

	poolConfig.MaxConns = cfg.MaxConns
	poolConfig.MinConns = cfg.MinConns
	poolConfig.MaxConnLifetime = cfg.MaxConnLifetime
	poolConfig.MaxConnIdleTime = cfg.MaxConnIdleTime

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return pool, nil
}

func runMigrations(databaseURL string) error {
	m, err := newMigrate(databaseURL)
	if err != nil {
		return err
	}

	if err := m.Up(); err != nil && err.Error() != "no change" {
		return err
	}

	return nil
}

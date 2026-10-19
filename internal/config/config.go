package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Server   ServerConfig
	Database DatabaseConfig
	Auth     AuthConfig
	SQS      SQSConfig
	Outbox   OutboxConfig
	Log      LogConfig
}

type ServerConfig struct {
	Port            int
	ShutdownTimeout time.Duration
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
}

type DatabaseConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	Name     string
	SSLMode  string
	MaxConns int32
	MinConns int32
}

func (d DatabaseConfig) DSN() string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?sslmode=%s",
		d.User, d.Password, d.Host, d.Port, d.Name, d.SSLMode,
	)
}

type AuthConfig struct {
	IssuerURL     string
	DiscoveryURL  string
	Audience      string
	ProviderClaim string
	InternalClaim string
	Enabled       bool
}

type SQSConfig struct {
	Region              string
	EndpointURL         string
	QueueURL            string
	DLQURL              string
	EventsQueueURL      string
	ConsumerName        string
	MaxMessages         int32
	WaitTimeSeconds     int32
	VisibilityTimeout   int32
	MaxReceiveCount     int
	WorkerCount         int
	ShutdownGracePeriod time.Duration
}

type OutboxConfig struct {
	BatchSize     int
	PollInterval  time.Duration
	LockDuration  time.Duration
	MaxAttempts   int
	BaseBackoff   time.Duration
	WorkerCount   int
	ReferenceTTL  time.Duration
	ReferenceMax  int
	ReferencePoll time.Duration
}

type LogConfig struct {
	Level  string
	Format string
}

func Load() (*Config, error) {
	cfg := &Config{
		Server: ServerConfig{
			Port:            envInt("SERVER_PORT", 8080),
			ShutdownTimeout: envDuration("SERVER_SHUTDOWN_TIMEOUT", 30*time.Second),
			ReadTimeout:     envDuration("SERVER_READ_TIMEOUT", 15*time.Second),
			WriteTimeout:    envDuration("SERVER_WRITE_TIMEOUT", 15*time.Second),
		},
		Database: DatabaseConfig{
			Host:     envStr("DB_HOST", "localhost"),
			Port:     envInt("DB_PORT", 5432),
			User:     envStr("DB_USER", "junglegaming"),
			Password: envStr("DB_PASSWORD", "junglegaming"),
			Name:     envStr("DB_NAME", "junglegaming"),
			SSLMode:  envStr("DB_SSLMODE", "disable"),
			MaxConns: int32(envInt("DB_MAX_CONNS", 20)),
			MinConns: int32(envInt("DB_MIN_CONNS", 2)),
		},
		Auth: AuthConfig{
			IssuerURL:     envStr("OIDC_ISSUER_URL", "http://localhost:8081/realms/junglegaming"),
			DiscoveryURL:  envStr("OIDC_DISCOVERY_URL", ""),
			Audience:      envStr("OIDC_AUDIENCE", "wagering-api"),
			ProviderClaim: envStr("OIDC_PROVIDER_CLAIM", "provider_id"),
			InternalClaim: envStr("OIDC_INTERNAL_CLAIM", "scope_internal"),
			Enabled:       envBool("AUTH_ENABLED", true),
		},
		SQS: SQSConfig{
			Region:              envStr("AWS_REGION", "us-east-1"),
			EndpointURL:         envStr("AWS_ENDPOINT_URL", "http://localhost:4566"),
			QueueURL:            envStr("SQS_QUEUE_URL", ""),
			DLQURL:              envStr("SQS_DLQ_URL", ""),
			EventsQueueURL:      envStr("SQS_EVENTS_QUEUE_URL", ""),
			ConsumerName:        envStr("SQS_CONSUMER_NAME", "wager-transactions-consumer"),
			MaxMessages:         int32(envInt("SQS_MAX_MESSAGES", 10)),
			WaitTimeSeconds:     int32(envInt("SQS_WAIT_TIME_SECONDS", 5)),
			VisibilityTimeout:   int32(envInt("SQS_VISIBILITY_TIMEOUT", 30)),
			MaxReceiveCount:     envInt("SQS_MAX_RECEIVE_COUNT", 5),
			WorkerCount:         envInt("SQS_WORKER_COUNT", 4),
			ShutdownGracePeriod: envDuration("SQS_SHUTDOWN_GRACE", 20*time.Second),
		},
		Outbox: OutboxConfig{
			BatchSize:     envInt("OUTBOX_BATCH_SIZE", 50),
			PollInterval:  envDuration("OUTBOX_POLL_INTERVAL", 1*time.Second),
			LockDuration:  envDuration("OUTBOX_LOCK_DURATION", 30*time.Second),
			MaxAttempts:   envInt("OUTBOX_MAX_ATTEMPTS", 10),
			BaseBackoff:   envDuration("OUTBOX_BASE_BACKOFF", 1*time.Second),
			WorkerCount:   envInt("OUTBOX_WORKER_COUNT", 1),
			ReferenceTTL:  envDuration("REFERENCE_TTL", 24*time.Hour),
			ReferenceMax:  envInt("REFERENCE_MAX_ATTEMPTS", 10),
			ReferencePoll: envDuration("REFERENCE_POLL_INTERVAL", 2*time.Second),
		},
		Log: LogConfig{
			Level:  envStr("LOG_LEVEL", "info"),
			Format: envStr("LOG_FORMAT", "json"),
		},
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("invalid server port: %d", c.Server.Port)
	}
	if c.Database.Host == "" || c.Database.Name == "" || c.Database.User == "" {
		return fmt.Errorf("database host, name and user are required")
	}
	if c.Database.MaxConns < 1 {
		return fmt.Errorf("DB_MAX_CONNS must be at least 1")
	}
	if c.Auth.Enabled && c.Auth.IssuerURL == "" {
		return fmt.Errorf("OIDC_ISSUER_URL is required when auth is enabled")
	}
	if c.Outbox.BatchSize < 1 {
		return fmt.Errorf("OUTBOX_BATCH_SIZE must be at least 1")
	}
	if c.Outbox.MaxAttempts < 1 {
		return fmt.Errorf("OUTBOX_MAX_ATTEMPTS must be at least 1")
	}
	return nil
}

func envStr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			return parsed
		}
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if parsed, err := strconv.ParseBool(v); err == nil {
			return parsed
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if parsed, err := time.ParseDuration(v); err == nil {
			return parsed
		}
		if secs, err := strconv.Atoi(v); err == nil {
			return time.Duration(secs) * time.Second
		}
	}
	return fallback
}

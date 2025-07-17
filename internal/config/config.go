package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	// Server configuration
	Host        string
	WSPort      string
	LogLevel    string
	Environment string

	// Database configuration
	DatabasePath string

	// Timeouts
	ShutdownTimeout time.Duration
	WriteTimeout    time.Duration
	ReadTimeout     time.Duration
	PingInterval    time.Duration
	PongWait        time.Duration

	// Limits and buffer sizes
	MaxConnections int
	MaxMessageSize int64
	BufferSize     int
}

func Load() (*Config, error) {
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("error loading .env file: %w", err)
	}

	cfg := &Config{
		Host:            getEnv("HOST", "localhost"),
		WSPort:          getEnv("WS_PORT", ":8080"),
		LogLevel:        getEnv("LOG_LEVEL", "info"),
		Environment:     getEnv("ENV", "development"),
		DatabasePath:    getEnv("DATABASE_PATH", "data.db"),
		ShutdownTimeout: getDurationEnv("SHUTDOWN_TIMEOUT", 30*time.Second),
		WriteTimeout:    getDurationEnv("WRITE_TIMEOUT", 10*time.Second),
		ReadTimeout:     getDurationEnv("READ_TIMEOUT", 10*time.Second),
		PingInterval:    getDurationEnv("PING_INTERVAL", 30*time.Second),
		PongWait:        getDurationEnv("PONG_WAIT", 60*time.Second),
		MaxConnections:  getIntEnv("MAX_CONNECTIONS", 1000),
		MaxMessageSize:  getInt64Env("MAX_MESSAGE_SIZE", 512*1024), // 512KB default
		BufferSize:      getIntEnv("BUFFER_SIZE", 256),
	}

	// Validate configuration
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("configuration validation failed: %w", err)
	}

	return cfg, nil
}

func (c *Config) validate() error {
	if c.MaxConnections <= 0 {
		return fmt.Errorf("MAX_CONNECTIONS must be positive, got %d", c.MaxConnections)
	}
	if c.MaxMessageSize <= 0 {
		return fmt.Errorf("MAX_MESSAGE_SIZE must be positive, got %d", c.MaxMessageSize)
	}
	if c.BufferSize <= 0 {
		return fmt.Errorf("BUFFER_SIZE must be positive, got %d", c.BufferSize)
	}
	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("SHUTDOWN_TIMEOUT must be positive")
	}
	if c.WriteTimeout <= 0 {
		return fmt.Errorf("WRITE_TIMEOUT must be positive")
	}
	if c.ReadTimeout <= 0 {
		return fmt.Errorf("READ_TIMEOUT must be positive")
	}
	if c.PingInterval <= 0 {
		return fmt.Errorf("PING_INTERVAL must be positive")
	}
	if c.PongWait <= 0 {
		return fmt.Errorf("PONG_WAIT must be positive")
	}
	return nil
}

func getEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

func getDurationEnv(key string, defaultValue time.Duration) time.Duration {
	if value, exists := os.LookupEnv(key); exists {
		if duration, err := time.ParseDuration(value); err == nil {
			return duration
		}
	}
	return defaultValue
}

func getIntEnv(key string, defaultValue int) int {
	if value, exists := os.LookupEnv(key); exists {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

func getInt64Env(key string, defaultValue int64) int64 {
	if value, exists := os.LookupEnv(key); exists {
		if intValue, err := strconv.ParseInt(value, 10, 64); err == nil {
			return intValue
		}
	}
	return defaultValue
}

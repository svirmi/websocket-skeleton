package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// Server settings
	Host         string
	WSPort       string
	Environment  string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	PingInterval time.Duration
	PongWait     time.Duration

	// Resource limits
	MaxMessageSize    int64
	MaxConnections    int
	BufferSize        int
	ProcessingWorkers int

	// Database settings
	DatabasePath     string
	DatabasePoolSize int

	// Bybit settings
	BybitSymbols           []string
	BybitReconnectInterval time.Duration
}

func Load() (*Config, error) {
	cfg := &Config{
		// Server defaults
		Host:         getEnvStr("HOST", ""),
		WSPort:       getEnvStr("WS_PORT", ":8080"),
		Environment:  getEnvStr("ENVIRONMENT", "development"),
		ReadTimeout:  time.Duration(getEnvInt("READ_TIMEOUT_SEC", 15)) * time.Second,
		WriteTimeout: time.Duration(getEnvInt("WRITE_TIMEOUT_SEC", 15)) * time.Second,
		PingInterval: time.Duration(getEnvInt("PING_INTERVAL_SEC", 30)) * time.Second,
		PongWait:     time.Duration(getEnvInt("PONG_WAIT_SEC", 60)) * time.Second,

		// Resource limits
		MaxMessageSize:    int64(getEnvInt("MAX_MESSAGE_SIZE", 512*1024)),
		MaxConnections:    getEnvInt("MAX_CONNECTIONS", 1000),
		BufferSize:        getEnvInt("BUFFER_SIZE", 256),
		ProcessingWorkers: getEnvInt("PROCESSING_WORKERS", 4),

		// Database settings
		DatabasePath:     getEnvStr("DATABASE_PATH", "data.db"),
		DatabasePoolSize: getEnvInt("DATABASE_POOL_SIZE", 10),

		// Bybit settings
		BybitSymbols:           strings.Split(getEnvStr("BYBIT_SYMBOLS", "BTCUSDT-22JUL25-123000-C,ETHUSDT-23JUL25-3400-P"), ","),
		BybitReconnectInterval: time.Duration(getEnvInt("BYBIT_RECONNECT_INTERVAL_SEC", 5)) * time.Second,
	}

	return cfg, nil
}

// getEnvStr retrieves a string value from environment variables with a default fallback
func getEnvStr(key string, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

// getEnvInt retrieves an integer value from environment variables with a default fallback
func getEnvInt(key string, defaultValue int) int {
	if value, exists := os.LookupEnv(key); exists {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

// getEnvBool retrieves a boolean value from environment variables with a default fallback
func getEnvBool(key string, defaultValue bool) bool {
	if value, exists := os.LookupEnv(key); exists {
		if boolValue, err := strconv.ParseBool(value); err == nil {
			return boolValue
		}
	}
	return defaultValue
}

// getEnvFloat retrieves a float64 value from environment variables with a default fallback
func getEnvFloat(key string, defaultValue float64) float64 {
	if value, exists := os.LookupEnv(key); exists {
		if floatValue, err := strconv.ParseFloat(value, 64); err == nil {
			return floatValue
		}
	}
	return defaultValue
}

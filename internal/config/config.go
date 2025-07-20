package config

import (
	"os"
	"strconv"
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
	ProcessingWorkers int // Number of concurrent processing workers

	// Database settings
	DatabasePath     string
	DatabasePoolSize int
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
		MaxMessageSize:    int64(getEnvInt("MAX_MESSAGE_SIZE", 512*1024)), // 512KB default
		MaxConnections:    getEnvInt("MAX_CONNECTIONS", 1000),
		BufferSize:        getEnvInt("BUFFER_SIZE", 256),
		ProcessingWorkers: getEnvInt("PROCESSING_WORKERS", 4), // Default to 4 workers

		// Database settings
		DatabasePath:     getEnvStr("DATABASE_PATH", "data.db"),
		DatabasePoolSize: getEnvInt("DATABASE_POOL_SIZE", 10),
	}

	return cfg, nil
}

func getEnvStr(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value, exists := os.LookupEnv(key); exists {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

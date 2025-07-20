package logger

import (
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func Init(environment string) {
	// Set logger time format
	zerolog.TimeFieldFormat = time.RFC3339Nano

	// Set global logger
	log.Logger = log.Output(zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: "2006-01-02 15:04:05.000",
		NoColor:    environment == "production",
	})

	// Set log level based on environment
	switch environment {
	case "development":
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	case "production":
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}

	log.Info().
		Str("environment", environment).
		Msg("Logger initialized")
}

// GetLogger returns a logger with component context
func GetLogger(component string) zerolog.Logger {
	return log.With().Str("component", component).Logger()
}

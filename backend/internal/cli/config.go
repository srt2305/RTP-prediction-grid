package cli

import (
	"os"
	"time"
)

type config struct {
	postgresDSN  string
	redisAddr    string
	symbol       string
	window       time.Duration
	freezeBefore time.Duration
	amendBefore  time.Duration
	cellWidth    float64
	boundedCells int
	rakeBps      int
}

func defaultConfig() config {
	return config{
		postgresDSN:  env("RTP_POSTGRES_DSN", "postgres://rtp:rtp@localhost:5432/rtp"),
		redisAddr:    env("RTP_REDIS_ADDR", "localhost:6379"),
		symbol:       "BTCUSDT",
		window:       60 * time.Second,
		freezeBefore: 10 * time.Second,
		amendBefore:  25 * time.Second,
		cellWidth:    50,
		boundedCells: 20,
		rakeBps:      250,
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

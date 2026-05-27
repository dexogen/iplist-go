package app

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

type Config struct {
	HTTPAddr  string
	ConfigSet string
	DataRoot  string

	DNSRuntimeDir            string
	DNSRefreshEnabled        bool
	DNSRefreshCron           string
	DNSRefreshTimezone       string
	DNSRefreshResolveTimeout time.Duration
	DNSRefreshConcurrency    int
	DNSRefreshIPv4           bool
	DNSRefreshIPv6           bool
	ExportCacheDir           string

	Debug bool
}

func ConfigFromEnv() Config {
	return Config{
		HTTPAddr:                 env("HTTP_ADDR", ":"+env("HTTP_PORT", "8080")),
		ConfigSet:                normalizeConfigSet(env("IPLIST_CONFIG_SET", "main")),
		DataRoot:                 env("IPLIST_DATA_ROOT", "."),
		DNSRuntimeDir:            env("IPLIST_DNS_RUNTIME_DIR", "runtime/dns"),
		DNSRefreshEnabled:        envBool("IPLIST_DNS_REFRESH_ENABLED", true),
		DNSRefreshCron:           env("IPLIST_DNS_REFRESH_CRON", "0 */12 * * *"),
		DNSRefreshTimezone:       env("IPLIST_DNS_REFRESH_TIMEZONE", "UTC"),
		DNSRefreshResolveTimeout: envDuration("IPLIST_DNS_REFRESH_TIMEOUT", 4*time.Second),
		DNSRefreshConcurrency:    envInt("IPLIST_DNS_REFRESH_CONCURRENCY", 8),
		DNSRefreshIPv4:           envBool("IPLIST_DNS_REFRESH_IP4", true),
		DNSRefreshIPv6:           envBool("IPLIST_DNS_REFRESH_IP6", true),
		ExportCacheDir:           env("IPLIST_EXPORT_CACHE_DIR", "runtime/export-cache"),
		Debug:                    strings.EqualFold(env("DEBUG", "false"), "true"),
	}
}

func (c Config) LogLevel() slog.Level {
	if c.Debug {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
		return parsed
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

func normalizeConfigSet(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "beta":
		return "beta"
	case "russia":
		return "russia"
	default:
		return "main"
	}
}

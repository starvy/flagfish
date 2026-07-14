package storage

import (
	"fmt"
	"os"
	"strings"
)

// ConfigFromEnv reads the object-storage configuration from the process environment. Credentials
// arrive here and nowhere else — never a file, never the config table. It is the storage backend's
// own configuration surface, kept next to the code that consumes it rather than in the shared Env.
func ConfigFromEnv() (S3Config, error) {
	cfg := S3Config{
		Endpoint:  getenv("FLAGFISH_S3_ENDPOINT"),
		Bucket:    getenv("FLAGFISH_S3_BUCKET"),
		Region:    envOr("FLAGFISH_S3_REGION", "us-east-1"),
		AccessKey: getenv("FLAGFISH_S3_ACCESS_KEY"),
		SecretKey: getenv("FLAGFISH_S3_SECRET_KEY"),
	}
	var err error
	if cfg.UseSSL, err = envBool("FLAGFISH_S3_USE_SSL", true); err != nil {
		return S3Config{}, err
	}
	if cfg.PathStyle, err = envBool("FLAGFISH_S3_PATH_STYLE", false); err != nil {
		return S3Config{}, err
	}
	return cfg, nil
}

func getenv(key string) string { return strings.TrimSpace(os.Getenv(key)) }

func envOr(key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}

// envBool accepts exactly "true"/"false" (or empty for the default): a typo is a boot error, not a
// silently-wrong toggle.
func envBool(key string, fallback bool) (bool, error) {
	switch v := getenv(key); v {
	case "":
		return fallback, nil
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("storage: %s=%q (want \"true\" or \"false\")", key, v)
	}
}

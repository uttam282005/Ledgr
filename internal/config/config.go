package config

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// Config holds runtime configuration settings.
type Config struct {
	DatabaseURL      string
	QADatabaseURL    string
	HTTPPort         string
	NvidiaAPIKey     string
	NvidiaNIMBaseURL string
	NvidiaNIMModel   string
	DatasetSeed      int64
	DatasetVersion   string
	EngineVersion    string
}

// Load loads configuration from environment variables with sensible defaults.
func Load() *Config {
	loadDotEnv(".env")

	defaultDBURL := "postgres://finance_app:finance_app_secret@127.0.0.1:5432/ai_finance_db?sslmode=disable"
	defaultQADBURL := "postgres://qa_readonly:qa_readonly_secret@127.0.0.1:5432/ai_finance_db?sslmode=disable"

	seedVal := int64(42)
	if envSeed := os.Getenv("SEED"); envSeed != "" {
		if parsed, err := strconv.ParseInt(envSeed, 10, 64); err == nil {
			seedVal = parsed
		}
	}

	nvidiaKey := os.Getenv("NVIDIA_API_KEY")
	if nvidiaKey == "" {
		nvidiaKey = os.Getenv("NIM_API_KEY")
	}

	nvidiaModel := os.Getenv("NVIDIA_NIM_MODEL")
	if nvidiaModel == "" {
		nvidiaModel = os.Getenv("NVIDIA_MODEL")
	}
	if nvidiaModel == "" {
		nvidiaModel = "meta/llama-3.2-11b-vision-instruct"
	}

	return &Config{
		DatabaseURL:      getEnv("DATABASE_URL", defaultDBURL),
		QADatabaseURL:    getEnv("QA_DATABASE_URL", defaultQADBURL),
		HTTPPort:         getEnv("HTTP_PORT", "8080"),
		NvidiaAPIKey:     nvidiaKey,
		NvidiaNIMBaseURL: getEnv("NVIDIA_NIM_BASE_URL", "https://integrate.api.nvidia.com/v1"),
		NvidiaNIMModel:   nvidiaModel,
		DatasetSeed:      seedVal,
		DatasetVersion:   getEnv("DATASET_VERSION", "v1.0.0"),
		EngineVersion:    getEnv("ENGINE_VERSION", "v1.0.0"),
	}
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func loadDotEnv(filepath string) {
	file, err := os.Open(filepath)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			k := strings.TrimSpace(parts[0])
			v := strings.TrimSpace(parts[1])
			v = strings.Trim(v, `"'`)
			if os.Getenv(k) == "" {
				_ = os.Setenv(k, v)
			}
		}
	}
}

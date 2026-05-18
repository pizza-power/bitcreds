package config

import (
	"bufio"
	"os"
	"strings"
)

// LoadEnv reads a .env file and sets any variables that aren't already set
// in the environment. This lets CLI flags and real env vars take precedence.
func LoadEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}

		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`)

		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}

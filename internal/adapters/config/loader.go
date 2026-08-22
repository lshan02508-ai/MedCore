package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func Load(path string) (Runtime, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Runtime{}, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		err = decodeJSON(data, &cfg)
	case ".yaml", ".yml":
		err = decodeYAML(data, &cfg)
	default:
		return Runtime{}, fmt.Errorf("unsupported config format %q: use .json, .yaml, or .yml", filepath.Ext(path))
	}
	if err != nil {
		return Runtime{}, fmt.Errorf("decode config: %w", err)
	}
	return cfg.Validate()
}

package schema

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// ParseConfig reads and validates an ownbase.yaml from r.
// Unknown YAML fields are rejected so typos surface immediately.
func ParseConfig(r io.Reader) (*OwnbaseConfig, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var cfg OwnbaseConfig
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate: %w", err)
	}
	return &cfg, nil
}

// ParseConfigFile is ParseConfig from a file path.
func ParseConfigFile(path string) (*OwnbaseConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseConfig(f)
}

// MarshalConfig serializes a config to YAML.
func MarshalConfig(cfg *OwnbaseConfig) ([]byte, error) {
	return yaml.Marshal(cfg)
}

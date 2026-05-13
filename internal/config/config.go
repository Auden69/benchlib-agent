package config

import (
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

type ConnectorType string

const (
	ConnectorPlex      ConnectorType = "plex"
	ConnectorNavidrome ConnectorType = "navidrome"
)

type LibraryConfig struct {
	ID        string `yaml:"id" json:"id"`
	Name      string `yaml:"name" json:"name"`
	MediaType string `yaml:"media_type" json:"media_type"` // MOVIES, SERIES, MUSIC
	Enabled   bool   `yaml:"enabled" json:"enabled"`
}

type ConnectorConfig struct {
	Type      ConnectorType   `yaml:"type" json:"type"`
	Name      string          `yaml:"name" json:"name"`
	URL       string          `yaml:"url" json:"url"`
	PublicURL string          `yaml:"public_url,omitempty" json:"public_url"`
	Token     string          `yaml:"token,omitempty" json:"token,omitempty"`       // Plex
	Username  string          `yaml:"username,omitempty" json:"username,omitempty"` // Navidrome
	Password  string          `yaml:"password,omitempty" json:"password,omitempty"` // Navidrome
	Libraries []LibraryConfig `yaml:"libraries" json:"libraries"`
}

type ScheduleConfig struct {
	Hour   int `yaml:"hour" json:"hour"`   // 0-23
	Minute int `yaml:"minute" json:"minute"` // 0-59
}

type Config struct {
	BenchlibAPIKey string            `yaml:"benchlib_api_key" json:"benchlib_api_key"`
	BenchlibAPIURL string            `yaml:"-" json:"-"` // set au runtime, non persisté
	PublicURL      string            `yaml:"public_url,omitempty" json:"public_url"`
	Port           int               `yaml:"port" json:"port"`
	Schedule       ScheduleConfig    `yaml:"schedule" json:"schedule"`
	Connectors     []ConnectorConfig `yaml:"connectors" json:"connectors"`
}

func Default() *Config {
	return &Config{

		Port:           8090,
		Schedule:       ScheduleConfig{Hour: 3, Minute: 0},
		Connectors:     []ConnectorConfig{},
	}
}

var (
	mu       sync.RWMutex
	filePath string
)

func Load(path string) (*Config, error) {
	mu.Lock()
	filePath = path
	mu.Unlock()

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		cfg := Default()
		return cfg, Save(cfg)
	}
	if err != nil {
		return nil, err
	}
	cfg := Default()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func Save(cfg *Config) error {
	mu.RLock()
	path := filePath
	mu.RUnlock()
	if path == "" {
		path = "config.yaml"
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}
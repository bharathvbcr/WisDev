package config

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Agent         AgentConfig         `yaml:"agent"`
	LLM           LLMConfig           `yaml:"llm"`
	Storage       StorageConfig       `yaml:"storage"`
	Observability ObservabilityConfig `yaml:"observability"`
	Server        ServerConfig        `yaml:"server"`
}

type AgentConfig struct {
	Mode        string `yaml:"mode"`
	MaxSteps    int    `yaml:"max_steps"`
	TokenBudget int64  `yaml:"token_budget"`
}

type LLMConfig struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	APIKey   string `yaml:"api_key"`
	BaseURL  string `yaml:"base_url"`
}

type StorageConfig struct {
	Type string `yaml:"type"`
	DSN  string `yaml:"dsn"`
}

type ObservabilityConfig struct {
	EnableOTEL   bool   `yaml:"enable_otel"`
	OTLPEndpoint string `yaml:"otlp_endpoint"`
}

type ServerConfig struct {
	HTTPPort    int    `yaml:"http_port"`
	GRPCPort    int    `yaml:"grpc_port"`
	SidecarHost string `yaml:"sidecar_host"`
	SidecarPort int    `yaml:"sidecar_port"`
}

var (
	globalConfig *Config
	configOnce   sync.Once
	configMu     sync.RWMutex
)

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	cfg.resolveEnvVars()
	cfg.SetDefaults()

	return &cfg, nil
}

func Get() *Config {
	configMu.RLock()
	defer configMu.RUnlock()
	return globalConfig
}

func Set(cfg *Config) {
	configMu.Lock()
	defer configMu.Unlock()
	globalConfig = cfg
}

func LoadOrGet(path string) (*Config, error) {
	var err error
	configOnce.Do(func() {
		var cfg *Config
		cfg, err = Load(path)
		if err == nil {
			Set(cfg)
		}
	})
	if err != nil {
		return nil, err
	}
	return Get(), nil
}

func (c *Config) resolveEnvVars() {
	if strings.HasPrefix(c.LLM.APIKey, "${") && strings.HasSuffix(c.LLM.APIKey, "}") {
		envVar := c.LLM.APIKey[2 : len(c.LLM.APIKey)-1]
		c.LLM.APIKey = os.Getenv(envVar)
	}
	if strings.HasPrefix(c.Storage.DSN, "${") && strings.HasSuffix(c.Storage.DSN, "}") {
		envVar := c.Storage.DSN[2 : len(c.Storage.DSN)-1]
		c.Storage.DSN = os.Getenv(envVar)
	}
}

func (c *Config) SetDefaults() {
	if c.Agent.Mode == "" {
		c.Agent.Mode = "autonomous"
	}
	if c.Agent.MaxSteps == 0 {
		c.Agent.MaxSteps = 15
	}
	if c.Agent.TokenBudget == 0 {
		c.Agent.TokenBudget = 100000
	}
	if c.LLM.Model == "" {
		c.LLM.Model = "gpt-4o"
	}
	if c.Storage.Type == "" {
		c.Storage.Type = "memory"
	}
	if c.Server.HTTPPort == 0 {
		c.Server.HTTPPort = 8081
	}
	if c.Server.GRPCPort == 0 {
		c.Server.GRPCPort = 50052
	}
	if c.Server.SidecarHost == "" {
		c.Server.SidecarHost = "localhost"
	}
	if c.Server.SidecarPort == 0 {
		c.Server.SidecarPort = 8000
	}
	if c.Observability.OTLPEndpoint == "" {
		c.Observability.OTLPEndpoint = "localhost:4317"
	}
}

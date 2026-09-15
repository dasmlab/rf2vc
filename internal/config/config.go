package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen      string     `yaml:"listen"`
	TLSCertFile string     `yaml:"tlsCertFile"`
	TLSKeyFile  string     `yaml:"tlsKeyFile"`
	Auth        AuthConfig `yaml:"auth"`
	DataDir     string     `yaml:"dataDir"`
	ISOCacheDir string     `yaml:"isoCacheDir"`
}

type AuthConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	applyEnvOverrides(&c)
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	if c.DataDir == "" {
		c.DataDir = "/data"
	}
	if c.ISOCacheDir == "" {
		c.ISOCacheDir = c.DataDir + "/iso-cache"
	}
	if c.Auth.Username == "" || c.Auth.Password == "" {
		return nil, fmt.Errorf("auth.username and auth.password are required")
	}
	return &c, nil
}

func applyEnvOverrides(c *Config) {
	if v := os.Getenv("RF2VC_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := os.Getenv("RF2VC_AUTH_USERNAME"); v != "" {
		c.Auth.Username = v
	}
	if v := os.Getenv("RF2VC_AUTH_PASSWORD"); v != "" {
		c.Auth.Password = v
	}
	if v := os.Getenv("RF2VC_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if v := os.Getenv("RF2VC_ISO_CACHE_DIR"); v != "" {
		c.ISOCacheDir = v
	}
}

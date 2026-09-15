package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen      string        `yaml:"listen"`
	TLSCertFile string        `yaml:"tlsCertFile"`
	TLSKeyFile  string        `yaml:"tlsKeyFile"`
	Auth        AuthConfig    `yaml:"auth"`
	VSphere     VSphereConfig `yaml:"vsphere"`
	ISOCacheDir string        `yaml:"isoCacheDir"`
}

type AuthConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type VSphereConfig struct {
	URL         string   `yaml:"url"`
	Username    string   `yaml:"username"`
	Password    string   `yaml:"password"`
	Insecure    bool     `yaml:"insecure"`
	Datacenter  string   `yaml:"datacenter"`
	Datastore   string   `yaml:"datastore"`
	ISOFolder   string   `yaml:"isoFolder"`
	VMAllowlist []string `yaml:"vmAllowlist"`
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
	if c.ISOCacheDir == "" {
		c.ISOCacheDir = "/var/tmp/rf2vc"
	}
	if c.VSphere.ISOFolder == "" {
		c.VSphere.ISOFolder = "rf2vc/isos"
	}
	if c.Auth.Username == "" || c.Auth.Password == "" {
		return nil, fmt.Errorf("auth.username and auth.password are required")
	}
	if c.VSphere.URL == "" || c.VSphere.Username == "" || c.VSphere.Password == "" {
		return nil, fmt.Errorf("vsphere.url/username/password are required")
	}
	if c.VSphere.Datacenter == "" || c.VSphere.Datastore == "" {
		return nil, fmt.Errorf("vsphere.datacenter and vsphere.datastore are required")
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
	if v := os.Getenv("RF2VC_VSPHERE_URL"); v != "" {
		c.VSphere.URL = v
	}
	if v := os.Getenv("RF2VC_VSPHERE_USERNAME"); v != "" {
		c.VSphere.Username = v
	}
	if v := os.Getenv("RF2VC_VSPHERE_PASSWORD"); v != "" {
		c.VSphere.Password = v
	}
	if v := os.Getenv("RF2VC_VSPHERE_DATACENTER"); v != "" {
		c.VSphere.Datacenter = v
	}
	if v := os.Getenv("RF2VC_VSPHERE_DATASTORE"); v != "" {
		c.VSphere.Datastore = v
	}
	if v := os.Getenv("RF2VC_ISO_CACHE_DIR"); v != "" {
		c.ISOCacheDir = v
	}
}

package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen string `yaml:"listen"`
	// UIListen, when set, moves the dashboard + /api to a second listener (e.g.
	// 127.0.0.1:8081 behind oauth-proxy); Listen then serves only Redfish.
	UIListen string `yaml:"uiListen"`
	// APIListen, when set, serves /api for system callers with Kubernetes
	// bearer tokens (see Auth.APIService), over TLS when APITLS* are set.
	APIListen      string     `yaml:"apiListen"`
	APITLSCertFile string     `yaml:"apiTLSCertFile"`
	APITLSKeyFile  string     `yaml:"apiTLSKeyFile"`
	TLSCertFile    string     `yaml:"tlsCertFile"`
	TLSKeyFile     string     `yaml:"tlsKeyFile"`
	Auth           AuthConfig `yaml:"auth"`
	DataDir        string     `yaml:"dataDir"`
	ISOCacheDir    string     `yaml:"isoCacheDir"`
}

type AuthConfig struct {
	// Username/Password is the shared (break-glass) account.
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	// RedfishClientsDir holds one file per Redfish client: file name = username,
	// content = password (a mounted Secret). Re-read periodically for rotation.
	RedfishClientsDir string `yaml:"redfishClientsDir"`
	// DisableSharedRedfish stops the shared account from working on /redfish
	// once every BMH uses its own client credential.
	DisableSharedRedfish bool `yaml:"disableSharedRedfish"`
	// APIService names the Service a bearer-token caller must be able to "get"
	// (in the pod's namespace).
	APIService string `yaml:"apiService"`
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
	if c.Auth.APIService == "" {
		c.Auth.APIService = "rf2vc"
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
	if v := os.Getenv("RF2VC_UI_LISTEN"); v != "" {
		c.UIListen = v
	}
	if v := os.Getenv("RF2VC_AUTH_USERNAME"); v != "" {
		c.Auth.Username = v
	}
	if v := os.Getenv("RF2VC_AUTH_PASSWORD"); v != "" {
		c.Auth.Password = v
	}
	if v := os.Getenv("RF2VC_REDFISH_CLIENTS_DIR"); v != "" {
		c.Auth.RedfishClientsDir = v
	}
	if v := os.Getenv("RF2VC_REDFISH_DISABLE_SHARED"); v != "" {
		c.Auth.DisableSharedRedfish = v == "true"
	}
	if v := os.Getenv("RF2VC_API_LISTEN"); v != "" {
		c.APIListen = v
	}
	if v := os.Getenv("RF2VC_API_TLS_CERT"); v != "" {
		c.APITLSCertFile = v
	}
	if v := os.Getenv("RF2VC_API_TLS_KEY"); v != "" {
		c.APITLSKeyFile = v
	}
	if v := os.Getenv("RF2VC_API_SERVICE"); v != "" {
		c.Auth.APIService = v
	}
	if v := os.Getenv("RF2VC_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if v := os.Getenv("RF2VC_ISO_CACHE_DIR"); v != "" {
		c.ISOCacheDir = v
	}
}

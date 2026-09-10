package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	configDirName  = "cnips"
	configFileName = "config.json"
)

type Config struct {
	CurrentProfile string             `json:"currentProfile"`
	Profiles       map[string]Profile `json:"profiles"`
}

type Profile struct {
	Name         string      `json:"name"`
	BaseURL      string      `json:"baseUrl,omitempty"`
	APIURL       string      `json:"apiUrl"`
	TenantKey    string      `json:"tenantKey"`
	Token        string      `json:"token"`
	RefreshToken string      `json:"refreshToken,omitempty"`
	TokenType    string      `json:"tokenType,omitempty"`
	ExpiresAt    time.Time   `json:"expiresAt,omitempty"`
	WorkspaceID  string      `json:"workspaceId,omitempty"`
	Workspaces   []Workspace `json:"workspaces,omitempty"`
	User         *UserInfo   `json:"user,omitempty"`
	UpdatedAt    time.Time   `json:"updatedAt"`
}

type Workspace struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

type UserInfo struct {
	Subject           string         `json:"subject,omitempty"`
	Email             string         `json:"email,omitempty"`
	Name              string         `json:"name,omitempty"`
	PreferredUsername string         `json:"preferredUsername,omitempty"`
	ClientID          string         `json:"clientId,omitempty"`
	Groups            []UserGroup    `json:"groups,omitempty"`
	Claims            map[string]any `json:"claims,omitempty"`
}

type UserGroup struct {
	GroupID   string   `json:"groupId,omitempty"`
	GroupType string   `json:"groupType,omitempty"`
	Roles     []string `json:"roles,omitempty"`
}

func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{Profiles: map[string]Profile{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse auth config %s: %w", path, err)
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]Profile{}
	}
	return &cfg, nil
}

func Save(cfg *Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]Profile{}
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func Path() (string, error) {
	if path := strings.TrimSpace(os.Getenv("CNIPS_CONFIG")); path != "" {
		return path, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, configDirName, configFileName), nil
}

func (c *Config) Current() (Profile, bool) {
	if c == nil || c.CurrentProfile == "" {
		return Profile{}, false
	}
	profile, ok := c.Profiles[c.CurrentProfile]
	return profile, ok
}

func (c *Config) Upsert(profile Profile) {
	if c.Profiles == nil {
		c.Profiles = map[string]Profile{}
	}
	if profile.Name == "" {
		profile.Name = "default"
	}
	profile.APIURL = strings.TrimRight(profile.APIURL, "/")
	profile.UpdatedAt = time.Now()
	c.Profiles[profile.Name] = profile
	c.CurrentProfile = profile.Name
}

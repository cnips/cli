package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	configDirName  = "cnips"
	configFileName = "config.json"
)

// Config persists logins as an array. Profiles remains an in-memory compatibility
// index for callers that still address a login by its friendly profile name.
type Config struct {
	CurrentProfile string             `json:"currentProfile,omitempty"`
	CurrentLogin   string             `json:"currentLogin,omitempty"`
	Logins         []Profile          `json:"logins"`
	Profiles       map[string]Profile `json:"-"`
}

type Profile struct {
	Name          string       `json:"name,omitempty"`
	UserID        string       `json:"userId,omitempty"`
	BaseURL       string       `json:"baseUrl,omitempty"`
	APIURL        string       `json:"apiUrl"`
	TenantKey     string       `json:"tenantKey,omitempty"`
	Token         string       `json:"token,omitempty"`
	RefreshToken  string       `json:"refreshToken,omitempty"`
	TokenType     string       `json:"tokenType,omitempty"`
	ExpiresAt     time.Time    `json:"expiresAt,omitempty"`
	WorkspaceID   string       `json:"workspaceId,omitempty"`
	WorkspaceName string       `json:"workspaceName,omitempty"`
	Workspaces    []Workspace  `json:"workspaces,omitempty"`
	Directory     string       `json:"directory,omitempty"`
	Directories   []string     `json:"directories,omitempty"`
	Repositories  []Repository `json:"repositories,omitempty"`
	User          *UserInfo    `json:"user,omitempty"`
	UpdatedAt     time.Time    `json:"updatedAt"`
}

type Workspace struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

type Repository struct {
	Directory   string `json:"directory"`
	WorkspaceID string `json:"workspaceId,omitempty"`
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

type configFile struct {
	CurrentProfile string             `json:"currentProfile,omitempty"`
	CurrentLogin   string             `json:"currentLogin,omitempty"`
	Logins         []Profile          `json:"logins,omitempty"`
	Profiles       map[string]Profile `json:"profiles,omitempty"` // legacy map format
}

func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return newConfig(), nil
	}
	if err != nil {
		return nil, err
	}
	var stored configFile
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("parse auth config %s: %w", path, err)
	}
	cfg := &Config{CurrentProfile: stored.CurrentProfile, CurrentLogin: stored.CurrentLogin, Logins: stored.Logins}
	if len(cfg.Logins) == 0 && len(stored.Profiles) > 0 {
		names := make([]string, 0, len(stored.Profiles))
		for name := range stored.Profiles {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			profile := stored.Profiles[name]
			if profile.Name == "" {
				profile.Name = name
			}
			cfg.Logins = append(cfg.Logins, normalizeProfile(profile))
		}
	}
	cfg.normalize()
	return cfg, nil
}

func Save(cfg *Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if cfg == nil {
		cfg = newConfig()
	}
	cfg.normalize()
	stored := configFile{CurrentProfile: cfg.CurrentProfile, CurrentLogin: cfg.CurrentLogin, Logins: cfg.Logins}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func Remove() error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
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

func newConfig() *Config { return &Config{Logins: []Profile{}, Profiles: map[string]Profile{}} }

func (c *Config) normalize() {
	if c.Logins == nil {
		c.Logins = []Profile{}
	}
	// Preserve callers constructing Config{Profiles: ...} directly.
	if len(c.Logins) == 0 && len(c.Profiles) > 0 {
		for _, profile := range c.Profiles {
			c.Logins = append(c.Logins, profile)
		}
	}
	oldCurrentLogin := c.CurrentLogin
	var normalized []Profile
	for _, profile := range c.Logins {
		normalized = append(normalized, splitProfileByWorkspace(normalizeProfile(profile))...)
	}
	c.Logins = mergeDuplicateLogins(normalized)
	if oldCurrentLogin != "" {
		c.CurrentLogin = ""
		for i := len(c.Logins) - 1; i >= 0; i-- {
			profile := c.Logins[i]
			if LoginKey(profile) == oldCurrentLogin || legacyLoginKey(profile) == oldCurrentLogin {
				c.CurrentLogin = LoginKey(profile)
				break
			}
		}
	}
	if c.CurrentLogin == "" && len(c.Logins) > 0 {
		for i := len(c.Logins) - 1; i >= 0; i-- {
			if c.CurrentProfile == "" || c.Logins[i].Name == c.CurrentProfile {
				c.CurrentLogin = LoginKey(c.Logins[i])
				break
			}
		}
	}
	c.reindex()
}

func normalizeProfile(profile Profile) Profile {
	if profile.Name == "" {
		profile.Name = "default"
	}
	profile.BaseURL = strings.TrimRight(strings.TrimSpace(profile.BaseURL), "/")
	profile.APIURL = strings.TrimRight(strings.TrimSpace(profile.APIURL), "/")
	if profile.UserID == "" && profile.User != nil {
		profile.UserID = strings.TrimSpace(profile.User.Subject)
	}
	profile.WorkspaceID = strings.TrimSpace(profile.WorkspaceID)
	profile.WorkspaceName = strings.TrimSpace(profile.WorkspaceName)
	if profile.WorkspaceName == "" {
		profile.WorkspaceName = workspaceNameForID(profile.Workspaces, profile.WorkspaceID)
	}
	if profile.Directory != "" {
		profile.Directories = appendDirectory(profile.Directories, profile.Directory)
		profile.Directory = cleanDirectory(profile.Directory)
	}
	for i := range profile.Directories {
		profile.Directories[i] = cleanDirectory(profile.Directories[i])
	}
	for _, dir := range profile.Directories {
		profile.Repositories = ensureRepository(profile.Repositories, dir, profile.WorkspaceID)
	}
	for i := range profile.Repositories {
		profile.Repositories[i].Directory = cleanDirectory(profile.Repositories[i].Directory)
		profile.Directories = appendDirectory(profile.Directories, profile.Repositories[i].Directory)
	}
	if profile.Directory == "" && len(profile.Directories) > 0 {
		profile.Directory = profile.Directories[len(profile.Directories)-1]
	}
	return profile
}

func splitProfileByWorkspace(profile Profile) []Profile {
	if len(profile.Repositories) == 0 {
		return []Profile{profile}
	}
	byWorkspace := make(map[string][]Repository)
	order := make([]string, 0)
	for _, repository := range profile.Repositories {
		workspaceID := strings.TrimSpace(repository.WorkspaceID)
		if workspaceID == "" {
			workspaceID = profile.WorkspaceID
		}
		if _, ok := byWorkspace[workspaceID]; !ok {
			order = append(order, workspaceID)
		}
		repository.WorkspaceID = workspaceID
		byWorkspace[workspaceID] = append(byWorkspace[workspaceID], repository)
	}
	if len(order) == 1 && order[0] == profile.WorkspaceID {
		return []Profile{profile}
	}
	out := make([]Profile, 0, len(order))
	for _, workspaceID := range order {
		copy := profile
		copy.WorkspaceID = workspaceID
		copy.WorkspaceName = workspaceNameForID(copy.Workspaces, workspaceID)
		copy.Repositories = append([]Repository(nil), byWorkspace[workspaceID]...)
		copy.Directories = nil
		copy.Directory = ""
		for _, repository := range copy.Repositories {
			copy.Directories = appendDirectory(copy.Directories, repository.Directory)
		}
		if len(copy.Directories) > 0 {
			copy.Directory = copy.Directories[len(copy.Directories)-1]
		}
		out = append(out, copy)
	}
	return out
}

func mergeDuplicateLogins(logins []Profile) []Profile {
	out := make([]Profile, 0, len(logins))
	index := make(map[string]int)
	for _, profile := range logins {
		profile = normalizeProfile(profile)
		key := LoginKey(profile)
		if i, ok := index[key]; ok {
			profile.Directories = mergeDirectories(out[i].Directories, profile.Directories)
			profile.Repositories = mergeRepositories(out[i].Repositories, profile.Repositories)
			out[i] = profile
			continue
		}
		index[key] = len(out)
		out = append(out, profile)
	}
	return out
}

func (c *Config) reindex() {
	c.Profiles = map[string]Profile{}
	for _, profile := range c.Logins {
		c.Profiles[profile.Name] = profile
	}
}

// LoginKey uses user ID + base URL + workspace name as the unique login identity.
func LoginKey(profile Profile) string {
	profile = normalizeProfile(profile)
	userID := profile.UserID
	if userID == "" {
		userID = "profile:" + profile.Name
	}
	baseURL := profile.BaseURL
	if baseURL == "" {
		baseURL = profile.APIURL
	}
	workspaceName := firstNonEmpty(profile.WorkspaceName, profile.WorkspaceID, "default")
	return strings.ToLower(userID) + "|" + strings.ToLower(strings.TrimRight(baseURL, "/")) + "|" + strings.ToLower(workspaceName)
}

func legacyLoginKey(profile Profile) string {
	profile = normalizeProfile(profile)
	userID := firstNonEmpty(profile.UserID, "profile:"+profile.Name)
	baseURL := firstNonEmpty(profile.BaseURL, profile.APIURL)
	return strings.ToLower(userID) + "|" + strings.ToLower(strings.TrimRight(baseURL, "/"))
}

func (c *Config) Current() (Profile, bool) {
	if c == nil {
		return Profile{}, false
	}
	for _, profile := range c.Logins {
		if c.CurrentLogin != "" && LoginKey(profile) == c.CurrentLogin {
			return profile, true
		}
	}
	for i := len(c.Logins) - 1; i >= 0; i-- {
		if c.Logins[i].Name == c.CurrentProfile {
			return c.Logins[i], true
		}
	}
	return Profile{}, false
}

func (c *Config) CurrentForDirectory(dir string) (Profile, bool) {
	if profile, ok := c.ForDirectory(dir); ok {
		return profile, true
	}
	return c.Current()
}

func (c *Config) ForDirectory(dir string) (Profile, bool) {
	if c == nil {
		return Profile{}, false
	}
	dir = cleanDirectory(dir)
	bestLength := -1
	var best Profile
	for _, profile := range c.Logins {
		for _, repository := range profile.Repositories {
			mapped := cleanDirectory(repository.Directory)
			if mapped != "" && directoryContains(mapped, dir) && len(mapped) > bestLength {
				best = profile
				if repository.WorkspaceID != "" {
					best.WorkspaceID = repository.WorkspaceID
				}
				bestLength = len(mapped)
			}
		}
	}
	if bestLength >= 0 {
		return best, true
	}
	return Profile{}, false
}

func (c *Config) Find(nameOrKey string) (Profile, bool) {
	nameOrKey = strings.TrimSpace(nameOrKey)
	if nameOrKey == "" {
		return c.Current()
	}
	for i := len(c.Logins) - 1; i >= 0; i-- {
		if c.Logins[i].Name == nameOrKey || LoginKey(c.Logins[i]) == nameOrKey {
			return c.Logins[i], true
		}
	}
	return Profile{}, false
}

func (c *Config) Upsert(profile Profile) { c.UpsertForDirectory(profile, "") }

func (c *Config) UpsertForDirectory(profile Profile, directory string) {
	if c == nil {
		return
	}
	profile = normalizeProfile(profile)
	profile.UpdatedAt = time.Now()
	if directory != "" {
		c.detachDirectory(directory, LoginKey(profile))
		profile.Directory = cleanDirectory(directory)
		profile.Directories = appendDirectory(profile.Directories, directory)
		profile.Repositories = upsertRepository(profile.Repositories, directory, profile.WorkspaceID)
	}
	key := LoginKey(profile)
	for i, existing := range c.Logins {
		if LoginKey(existing) != key {
			continue
		}
		profile.Directories = mergeDirectories(existing.Directories, profile.Directories)
		profile.Repositories = mergeRepositories(existing.Repositories, profile.Repositories)
		c.Logins[i] = profile
		c.CurrentProfile, c.CurrentLogin = profile.Name, key
		c.reindex()
		return
	}
	c.Logins = append(c.Logins, profile)
	c.CurrentProfile, c.CurrentLogin = profile.Name, key
	c.reindex()
}

func (c *Config) detachDirectory(directory, exceptKey string) {
	directory = cleanDirectory(directory)
	for i := range c.Logins {
		if LoginKey(c.Logins[i]) == exceptKey {
			continue
		}
		var dirs []string
		for _, mapped := range c.Logins[i].Directories {
			if cleanDirectory(mapped) != directory {
				dirs = append(dirs, mapped)
			}
		}
		var repositories []Repository
		for _, repository := range c.Logins[i].Repositories {
			if cleanDirectory(repository.Directory) != directory {
				repositories = append(repositories, repository)
			}
		}
		c.Logins[i].Directories = dirs
		c.Logins[i].Repositories = repositories
		syncPrimaryDirectory(&c.Logins[i])
	}
}

func (c *Config) Update(profile Profile) bool {
	key := LoginKey(profile)
	for i := range c.Logins {
		if LoginKey(c.Logins[i]) == key {
			c.Logins[i] = normalizeProfile(profile)
			c.CurrentProfile, c.CurrentLogin = profile.Name, key
			c.reindex()
			return true
		}
	}
	return false
}

func (c *Config) ClearToken(nameOrKey string) bool {
	profile, ok := c.Find(nameOrKey)
	if !ok {
		return false
	}
	profile.Token, profile.RefreshToken, profile.TokenType = "", "", ""
	profile.ExpiresAt = time.Time{}
	profile.UpdatedAt = time.Now()
	return c.Update(profile)
}

func (c *Config) SetWorkspace(nameOrKey, workspaceID string) bool {
	profile, ok := c.Find(nameOrKey)
	if !ok {
		return false
	}
	profile.WorkspaceID = strings.TrimSpace(workspaceID)
	profile.WorkspaceName = workspaceNameForID(profile.Workspaces, profile.WorkspaceID)
	profile.UpdatedAt = time.Now()
	if !c.replaceLogin(nameOrKey, profile) {
		return false
	}
	c.Logins = mergeDuplicateLogins(c.Logins)
	c.CurrentProfile, c.CurrentLogin = profile.Name, LoginKey(profile)
	c.reindex()
	return true
}

func (c *Config) SetWorkspaceForDirectory(nameOrKey, directory, workspaceID string) bool {
	profile, ok := c.Find(nameOrKey)
	if !ok {
		return false
	}
	directory = cleanDirectory(directory)
	if directory == "" {
		return false
	}
	c.detachDirectory(directory, "")
	profile.WorkspaceID = strings.TrimSpace(workspaceID)
	profile.WorkspaceName = workspaceNameForID(profile.Workspaces, profile.WorkspaceID)
	profile.Directory = directory
	profile.Directories = []string{directory}
	profile.Repositories = []Repository{{Directory: directory, WorkspaceID: profile.WorkspaceID}}
	profile.UpdatedAt = time.Now()
	c.UpsertForDirectory(profile, directory)
	return true
}

func (c *Config) replaceLogin(oldNameOrKey string, profile Profile) bool {
	old, ok := c.Find(oldNameOrKey)
	if !ok {
		return false
	}
	oldKey := LoginKey(old)
	for i := range c.Logins {
		if LoginKey(c.Logins[i]) == oldKey {
			c.Logins[i] = normalizeProfile(profile)
			c.CurrentProfile, c.CurrentLogin = profile.Name, LoginKey(profile)
			c.reindex()
			return true
		}
	}
	return false
}

// RemoveDirectory disconnects only the login associated with a repository. A
// login used by other repositories is retained with those mappings.
func (c *Config) RemoveDirectory(dir string) bool {
	dir = cleanDirectory(dir)
	profile, ok := c.ForDirectory(dir)
	if !ok {
		profile, ok = c.Current()
		if !ok || len(profile.Directories) > 0 {
			return false
		}
	}
	key := LoginKey(profile)
	for i := range c.Logins {
		if LoginKey(c.Logins[i]) != key {
			continue
		}
		var remaining []string
		for _, mapped := range c.Logins[i].Directories {
			if cleanDirectory(mapped) != dir {
				remaining = append(remaining, mapped)
			}
		}
		var remainingRepos []Repository
		for _, repository := range c.Logins[i].Repositories {
			if cleanDirectory(repository.Directory) != dir {
				remainingRepos = append(remainingRepos, repository)
			}
		}
		if len(remaining) > 0 {
			c.Logins[i].Directories = remaining
			c.Logins[i].Repositories = remainingRepos
			syncPrimaryDirectory(&c.Logins[i])
		} else {
			c.Logins = append(c.Logins[:i], c.Logins[i+1:]...)
		}
		if c.CurrentLogin == key {
			c.CurrentLogin, c.CurrentProfile = "", ""
			if len(c.Logins) > 0 {
				last := c.Logins[len(c.Logins)-1]
				c.CurrentLogin, c.CurrentProfile = LoginKey(last), last.Name
			}
		}
		c.reindex()
		return true
	}
	return false
}

func cleanDirectory(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err == nil {
		dir = abs
	}
	return filepath.Clean(dir)
}

func appendDirectory(dirs []string, dir string) []string {
	dir = cleanDirectory(dir)
	if dir == "" {
		return dirs
	}
	for _, existing := range dirs {
		if cleanDirectory(existing) == dir {
			return dirs
		}
	}
	return append(dirs, dir)
}

func mergeDirectories(left, right []string) []string {
	out := append([]string{}, left...)
	for _, dir := range right {
		out = appendDirectory(out, dir)
	}
	return out
}

func upsertRepository(repositories []Repository, directory, workspaceID string) []Repository {
	directory = cleanDirectory(directory)
	if directory == "" {
		return repositories
	}
	for i := range repositories {
		if cleanDirectory(repositories[i].Directory) == directory {
			repositories[i].Directory = directory
			if workspaceID != "" {
				repositories[i].WorkspaceID = strings.TrimSpace(workspaceID)
			}
			return repositories
		}
	}
	return append(repositories, Repository{Directory: directory, WorkspaceID: strings.TrimSpace(workspaceID)})
}

func ensureRepository(repositories []Repository, directory, workspaceID string) []Repository {
	directory = cleanDirectory(directory)
	for _, repository := range repositories {
		if cleanDirectory(repository.Directory) == directory {
			return repositories
		}
	}
	return upsertRepository(repositories, directory, workspaceID)
}

func mergeRepositories(left, right []Repository) []Repository {
	out := append([]Repository{}, left...)
	for _, repository := range right {
		out = upsertRepository(out, repository.Directory, repository.WorkspaceID)
	}
	return out
}

func syncPrimaryDirectory(profile *Profile) {
	if profile == nil {
		return
	}
	profile.Directory = ""
	if len(profile.Directories) > 0 {
		profile.Directory = cleanDirectory(profile.Directories[len(profile.Directories)-1])
	}
}

func directoryContains(root, dir string) bool {
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func workspaceNameForID(workspaces []Workspace, workspaceID string) string {
	workspaceID = strings.TrimSpace(workspaceID)
	for _, workspace := range workspaces {
		if strings.TrimSpace(workspace.ID) == workspaceID {
			return firstNonEmpty(workspace.Name, workspace.ID)
		}
	}
	return workspaceID
}

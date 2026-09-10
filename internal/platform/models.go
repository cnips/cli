// Package platform contains lightweight Go structs that mirror the JSON
// shapes returned by the cnips mgmt-srv REST API.
// We define these locally so the CLI has no compile-time dependency on the
// internal service modules.
package platform

import "strings"

// -----------------------------------------------------------------------
// Response envelopes
// -----------------------------------------------------------------------

// APIResponse is the top-level wrapper for every mgmt-srv response.
// { "success": true, "status": 200, "data": { ... } }
type APIResponse[T any] struct {
	Success bool   `json:"success"`
	Status  int    `json:"status"`
	Data    *T     `json:"data"`
	Error   *Error `json:"error,omitempty"`
}

// ListData wraps paginated list responses.
// { "list": [...], "count": 5 }
type ListData[T any] struct {
	List  []T `json:"list"`
	Count int `json:"count"`
}

type Error struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

type Manifest struct {
	ID            string                  `json:"id,omitempty"`
	WorkspaceID   string                  `json:"workspaceId,omitempty"`
	ComponentType string                  `json:"componentType,omitempty"`
	Files         map[string]ManifestFile `json:"files,omitempty"`
}

type ManifestFile struct {
	Digest   string `json:"digest,omitempty"`
	CodeHash string `json:"codeHash,omitempty"`
}

// Workspace mirrors confmodels.UserWorkspaceMap from mgmt-srv's /workspace
// endpoint. The platform has used both id and workspaceId fields.
type Workspace struct {
	ID            string `json:"id"`
	WorkspaceID   string `json:"workspaceId"`
	WorkspaceName string `json:"workspaceName"`
	Name          string `json:"name"`
}

// -----------------------------------------------------------------------
// Pipeline
// -----------------------------------------------------------------------

type Pipeline struct {
	ID                 string              `json:"id,omitempty"`
	AltID              string              `json:"_id,omitempty"`
	Name               string              `json:"name"`
	Description        string              `json:"description"`
	WorkspaceID        string              `json:"workspaceId"`
	Active             bool                `json:"active"`
	SourceList         []PipelineComponent `json:"sourceList"`
	DestinationList    []PipelineComponent `json:"destinationList"`
	TransformationList []PipelineComponent `json:"transformationList"`
	ApprovalList       []PipelineComponent `json:"approvalList"`
	AIConvAgentList    []PipelineComponent `json:"aiConvAgentList"`
	TransformationMap  *TransformationMap  `json:"transformationMap"`
	Schedule           *Schedule           `json:"schedule"`
	RetrySchedule      *RetrySchedule      `json:"retrySchedule"`
	GlobalVariables    []string            `json:"globalVariables"`
	Variables          []string            `json:"variables"`
	Language           string              `json:"language"`
	SignatureVersion   string              `json:"signatureVersion"`
}

type PipelineComponent struct {
	ID           string         `json:"id"`     // node graph UUID
	NodeID       string         `json:"nodeId"` // actual entity ID (transformation/source/destination)
	Name         string         `json:"name"`
	Type         string         `json:"type"` // TRANSFORMATION, DECISION, SWITCH, etc.
	UsedVersion  string         `json:"usedVersion"`
	IsApp        bool           `json:"isApp"`
	AppType      string         `json:"appType"`
	Config       map[string]any `json:"config"`
	ParentID     string         `json:"parentId"` // for loop body nodes
	NativeConfig any            `json:"nativeConfig"`
	LoopCount    int            `json:"loopCount"`
	SwitchLabels []string       `json:"switchLabels"`
	TimeoutMS    int            `json:"timeoutMS"`
}

type TransformationMap struct {
	EdgeMap       []EdgeMap           `json:"edgeMap"`
	NodePositions map[string]Position `json:"nodePositions"`
}

type EdgeMap struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Label  any    `json:"label"` // bool for decision, string for switch, nil for plain
}

type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type Schedule struct {
	CronExpression string `json:"cronExpression"`
	TimeZone       string `json:"timeZone"`
	Active         bool   `json:"active"`
}

type RetrySchedule struct {
	AfterSeconds int `json:"afterSeconds"`
	MaxRetries   int `json:"maxRetries"`
}

// -----------------------------------------------------------------------
// Source
// -----------------------------------------------------------------------

type Source struct {
	ID               string        `json:"id,omitempty"`
	AltID            string        `json:"_id,omitempty"`
	Name             string        `json:"name"`
	WorkspaceID      string        `json:"workspaceId"`
	PluginFileName   string        `json:"pluginFileName,omitempty"`
	SourceType       string        `json:"sourceType"` // STANDARD | EXTRACTOR
	Description      string        `json:"description"`
	Language         string        `json:"language"`
	SignatureVersion string        `json:"signatureVersion"`
	TemplateVersion  string        `json:"templateVersion"`
	APIAccessRef     string        `json:"apiAccessRef"`
	Config           []ConfigItem  `json:"config"`
	Versions         []VersionInfo `json:"versions,omitempty"`
	Schema           *SchemaRef    `json:"schema"`
	Schedule         *Schedule     `json:"schedule"`
	Active           bool          `json:"active"`
	SourceCode
}

func (s Source) NormalizedSourceType() string {
	sourceType := strings.ToUpper(strings.TrimSpace(s.SourceType))
	if sourceType == "" {
		return "SOURCE"
	}
	return sourceType
}

func (s Source) IsExtractor() bool {
	return s.NormalizedSourceType() == "EXTRACTOR"
}

// -----------------------------------------------------------------------
// Transformation
// -----------------------------------------------------------------------

type Transformation struct {
	ID               string        `json:"id,omitempty"`
	AltID            string        `json:"_id,omitempty"`
	Name             string        `json:"name"`
	WorkspaceID      string        `json:"workspaceId"`
	PluginFileName   string        `json:"pluginFileName,omitempty"`
	Type             string        `json:"type"` // TRANSFORMATION, DECISION, SWITCH, APPROVAL, LOOP
	Description      string        `json:"description"`
	Language         string        `json:"language"`
	SignatureVersion string        `json:"signatureVersion"`
	TemplateVersion  string        `json:"templateVersion"`
	Config           []ConfigItem  `json:"config"`
	Versions         []VersionInfo `json:"versions,omitempty"`
	SwitchLabels     []string      `json:"switchLabels"`
	Schema           *SchemaRef    `json:"schema"`
	OutputSchema     *SchemaRef    `json:"outputSchema"`
	SourceCode
}

// -----------------------------------------------------------------------
// Destination
// -----------------------------------------------------------------------

type Destination struct {
	ID               string        `json:"id,omitempty"`
	AltID            string        `json:"_id,omitempty"`
	Name             string        `json:"name"`
	WorkspaceID      string        `json:"workspaceId"`
	PluginFileName   string        `json:"pluginFileName,omitempty"`
	Description      string        `json:"description"`
	Language         string        `json:"language"`
	SignatureVersion string        `json:"signatureVersion"`
	TemplateVersion  string        `json:"templateVersion"`
	Config           []ConfigItem  `json:"config"`
	Versions         []VersionInfo `json:"versions,omitempty"`
	Schema           *SchemaRef    `json:"schema"`
	SourceCode
}

// -----------------------------------------------------------------------
// Function
// -----------------------------------------------------------------------

type Function struct {
	ID               string        `json:"id,omitempty"`
	AltID            string        `json:"_id,omitempty"`
	Name             string        `json:"name"`
	Alias            string        `json:"alias"`
	WorkspaceID      string        `json:"workspaceId"`
	PluginFileName   string        `json:"pluginFileName,omitempty"`
	Description      string        `json:"description"`
	Language         string        `json:"language"`
	SignatureVersion string        `json:"signatureVersion"`
	TemplateVersion  string        `json:"templateVersion"`
	APIAccessRef     string        `json:"apiAccessRef"`
	Config           []ConfigItem  `json:"config"`
	Versions         []VersionInfo `json:"versions,omitempty"`
	TimeoutMS        int64         `json:"timeoutMS"`
	Active           bool          `json:"active"`
	SourceCode
}

type GlobalVariable struct {
	ID            string `json:"id,omitempty"`
	AltID         string `json:"_id,omitempty"`
	Key           string `json:"key"`
	Value         string `json:"value"`
	WorkspaceID   string `json:"workspaceId"`
	ExpireSeconds int    `json:"expirySeconds"`
}

type Configuration struct {
	ID          string         `json:"id,omitempty"`
	AltID       string         `json:"_id,omitempty"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Type        string         `json:"type"`
	Key         string         `json:"key"`
	Value       string         `json:"value"`
	APIAccess   map[string]any `json:"apiAccess"`
	Active      bool           `json:"active"`
	WorkspaceID string         `json:"workspaceId"`
}

type App struct {
	ID               string        `json:"id,omitempty"`
	AltID            string        `json:"_id,omitempty"`
	Name             string        `json:"name,omitempty"`
	Description      string        `json:"description,omitempty"`
	Type             string        `json:"type,omitempty"`
	LogoURL          string        `json:"logoUrl,omitempty"`
	Tags             []string      `json:"tags,omitempty"`
	AppType          string        `json:"appType,omitempty"`
	Repo             string        `json:"repo,omitempty"`
	LatestVersion    string        `json:"latestVersion,omitempty"`
	LatestVersionID  string        `json:"latestVersionId,omitempty"`
	Versions         []VersionInfo `json:"versions,omitempty"`
	MarketplaceAppID string        `json:"marketplaceAppId,omitempty"`
	Language         string        `json:"language,omitempty"`
	Config           []ConfigItem  `json:"config,omitempty"`
	SourceCode
}

type VersionInfo struct {
	Version        string `json:"version,omitempty"`
	VersionNumber  string `json:"versionNumber,omitempty"`
	VersionID      string `json:"versionId,omitempty"`
	PluginFileName string `json:"pluginFileName,omitempty"`
}

// -----------------------------------------------------------------------
// Shared
// -----------------------------------------------------------------------

// SourceCode mirrors the inline source code fields from the platform models.
type SourceCode struct {
	Script      string      `json:"script"`
	Golang      *Golang     `json:"golang"`
	Python      *Python     `json:"python"`
	JavaScript  *JavaScript `json:"javascript"`
	BuildStatus string      `json:"buildStatus,omitempty"`
	IsBunBuild  bool        `json:"isBunBuild"`
}

type Version struct {
	ID               string `json:"id,omitempty"`
	AltID            string `json:"_id,omitempty"`
	Ref              string `json:"ref"`
	Name             string `json:"objectName"`
	WorkspaceID      string `json:"workspaceId"`
	Version          string `json:"version"`
	Type             string `json:"type"`
	Language         string `json:"language"`
	PluginFileName   string `json:"pluginFileName"`
	SignatureVersion string `json:"signatureVersion"`
	SourceCode
}

type AppVersion struct {
	ID               string       `json:"id,omitempty"`
	AltID            string       `json:"_id,omitempty"`
	Ref              string       `json:"ref,omitempty"`
	Name             string       `json:"appName,omitempty"`
	Version          string       `json:"version,omitempty"`
	Type             string       `json:"type,omitempty"`
	Language         string       `json:"language,omitempty"`
	Description      string       `json:"description,omitempty"`
	SignatureVersion string       `json:"signatureVersion,omitempty"`
	TemplateVersion  string       `json:"templateVersion,omitempty"`
	BuildStatus      string       `json:"buildStatus,omitempty"`
	Config           []ConfigItem `json:"config,omitempty"`
	SourceCode
}

type PublishRequest struct {
	ComponentID        string         `json:"componentId,omitempty"`
	ComponentName      string         `json:"componentName,omitempty"`
	ComponentType      string         `json:"componentType,omitempty"`
	WorkspaceID        string         `json:"workspaceId,omitempty"`
	VersionID          string         `json:"versionId,omitempty"`
	Version            string         `json:"version,omitempty"`
	MarketplaceVersion string         `json:"marketplaceVersion,omitempty"`
	Description        string         `json:"description,omitempty"`
	LogoURL            string         `json:"logoUrl,omitempty"`
	UserConfigs        []UserConfig   `json:"userConfigs,omitempty"`
	Language           string         `json:"language,omitempty"`
	PluginFileName     string         `json:"pluginFileName,omitempty"`
	BuildStatus        string         `json:"buildStatus,omitempty"`
	Code               *CodeSnapshot  `json:"code,omitempty"`
	SignatureVersion   string         `json:"signatureVersion,omitempty"`
	IsBunBuild         bool           `json:"isBunBuild"`
	MaskPublisherName  bool           `json:"maskPublisherName,omitempty"`
	SwitchLabels       []string       `json:"switchLabels,omitempty"`
	ApprovalConfig     map[string]any `json:"approvalConfig,omitempty"`
}

type PublishResponse struct {
	ID             string `json:"id,omitempty"`
	AltID          string `json:"_id,omitempty"`
	AppID          string `json:"appId,omitempty"`
	Version        string `json:"version,omitempty"`
	ArtifactStatus string `json:"artifactStatus,omitempty"`
	Status         string `json:"status,omitempty"`
}

type PublishStatus struct {
	ID             string `json:"id,omitempty"`
	ArtifactStatus string `json:"artifactStatus,omitempty"`
	ArtifactError  string `json:"artifactError,omitempty"`
}

type UserConfig struct {
	Key    string `json:"key,omitempty"`
	Value  string `json:"value,omitempty"`
	Secret bool   `json:"secret,omitempty"`
}

type CodeSnapshot struct {
	Script     string          `json:"script,omitempty"`
	Golang     *Golang         `json:"golang,omitempty"`
	Python     *Python         `json:"python,omitempty"`
	JavaScript *JavaScriptCode `json:"javascript,omitempty"`
}

type JavaScriptCode struct {
	Script      string `json:"script,omitempty"`
	PackageJSON string `json:"packageJson,omitempty"`
}

type Golang struct {
	Main string `json:"main"`
	Mod  string `json:"mod"`
}

type Python struct {
	Main         string `json:"main"`
	Requirements string `json:"requirements"`
}

type JavaScript struct {
	PackageJSON string `json:"packageJson"`
}

type ConfigItem struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type SchemaRef struct {
	ID          string `json:"id"`
	UsedVersion string `json:"usedVersion"`
}

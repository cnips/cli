package artifact

import "strings"

// Kind constants
const (
	KindProject        = "Project"
	KindPipeline       = "Pipeline"
	KindFunction       = "Function"
	KindComponent      = "Component"
	KindEnvironment    = "Environment"
	KindGlobalVariable = "GlobalVariable"
	KindConfiguration  = "Configuration"
	KindLock           = "Lock"
	KindCassette       = "Cassette"
)

// -----------------------------------------------------------------------
// Project manifest  (cnips.yaml)
// -----------------------------------------------------------------------

type Project struct {
	APIVersion string      `yaml:"apiVersion"`
	Kind       string      `yaml:"kind"`
	Metadata   ObjectMeta  `yaml:"metadata"`
	Spec       ProjectSpec `yaml:"spec"`
}

type ProjectSpec struct {
	Runtime      string            `yaml:"runtime,omitempty"`
	Registry     string            `yaml:"registry,omitempty"`
	Dependencies map[string]string `yaml:"dependencies,omitempty"` // name -> semver constraint
}

// -----------------------------------------------------------------------
// Lock file  (cnips.lock)
// -----------------------------------------------------------------------

type Lock struct {
	APIVersion string   `yaml:"apiVersion"`
	Kind       string   `yaml:"kind"`
	Spec       LockSpec `yaml:"spec"`
}

type LockSpec struct {
	Runtime    string          `yaml:"runtime,omitempty"`
	Components []LockComponent `yaml:"components,omitempty"`
}

type LockComponent struct {
	Name             string `yaml:"name"`
	Kind             string `yaml:"kind,omitempty"` // source, destination, transformation, function
	Version          string `yaml:"version"`
	Source           string `yaml:"source"` // marketplace | local
	Path             string `yaml:"path,omitempty"`
	Language         string `yaml:"language,omitempty"`
	SignatureVersion string `yaml:"signatureVersion,omitempty"`
	TemplateVersion  string `yaml:"templateVersion,omitempty"`
	ArtifactID       string `yaml:"artifactId,omitempty"`
	PluginFileName   string `yaml:"pluginFileName,omitempty"`
	IsBunBuild       bool   `yaml:"isBunBuild,omitempty"`
	Integrity        string `yaml:"integrity,omitempty"`
	Publisher        string `yaml:"publisher,omitempty"`
}

// -----------------------------------------------------------------------
// Pipeline  (pipelines/<name>/pipeline.yaml)
// -----------------------------------------------------------------------

type Pipeline struct {
	APIVersion string          `yaml:"apiVersion"`
	Kind       string          `yaml:"kind"`
	Metadata   ObjectMeta      `yaml:"metadata"`
	Spec       PipelineSpec    `yaml:"spec"`
	Layout     *PipelineLayout `yaml:"-"`
}

type PipelineSpec struct {
	Description string       `yaml:"description,omitempty"`
	Trigger     *TriggerSpec `yaml:"trigger,omitempty"`
	Schedule    string       `yaml:"schedule,omitempty"` // cron expression shorthand
	Retry       *RetryConfig `yaml:"retry,omitempty"`
	Steps       []Step       `yaml:"steps"`
	Variables   []VarRef     `yaml:"variables,omitempty"`
}

type TriggerSpec struct {
	Type      string `yaml:"type"`                // webhook, schedule, manual
	SourceRef string `yaml:"sourceRef,omitempty"` // slug of the source entity
	Path      string `yaml:"path,omitempty"`
	Auth      string `yaml:"auth,omitempty"`
	Schedule  string `yaml:"schedule,omitempty"` // cron expression
}

// Step represents one node in the pipeline graph.
// Both native and custom component steps share this structure.
type Step struct {
	ID       string            `yaml:"id"`
	Uses     string            `yaml:"uses"` // e.g. "native/decision@1" or "transformation/my-transform@v1"
	With     map[string]any    `yaml:"with,omitempty"`
	Next     string            `yaml:"next,omitempty"`     // default next step
	Branches map[string]string `yaml:"branches,omitempty"` // decision: true/false -> step ID
	Cases    map[string]string `yaml:"cases,omitempty"`    // switch: label -> step ID
	Retry    *RetryConfig      `yaml:"retry,omitempty"`
	OnError  string            `yaml:"onError,omitempty"` // fail | continue | skip
}

type RetryConfig struct {
	MaxAttempts int `yaml:"maxAttempts"`
	BackoffSec  int `yaml:"backoffSec,omitempty"`
}

type VarRef struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value,omitempty"`
	Ref   string `yaml:"ref,omitempty"`
}

type PipelineLayout struct {
	APIVersion string                   `yaml:"apiVersion"`
	Kind       string                   `yaml:"kind"`
	Positions  map[string]PipelinePoint `yaml:"positions,omitempty"`
}

type PipelinePoint struct {
	X float64 `yaml:"x"`
	Y float64 `yaml:"y"`
}

// -----------------------------------------------------------------------
// Function  (functions/<name>/cnips.fn.yaml)
// -----------------------------------------------------------------------

type Function struct {
	APIVersion string       `yaml:"apiVersion"`
	Kind       string       `yaml:"kind"`
	Metadata   ObjectMeta   `yaml:"metadata"`
	Spec       FunctionSpec `yaml:"spec"`
}

type FunctionSpec struct {
	Runtime          string            `yaml:"runtime"` // js, go, python
	Type             string            `yaml:"type,omitempty"`
	HTTPMethod       string            `yaml:"httpMethod,omitempty"`
	Description      string            `yaml:"description,omitempty"`
	Handler          string            `yaml:"handler,omitempty"` // ./handler.js#default
	Timeout          string            `yaml:"timeout,omitempty"`
	Memory           string            `yaml:"memory,omitempty"`
	Config           map[string]string `yaml:"config,omitempty"`
	APIAccessRef     string            `yaml:"apiAccessRef,omitempty"`
	TemplateID       string            `yaml:"templateId,omitempty"`
	SignatureVersion string            `yaml:"signatureVersion,omitempty"`
	TemplateVersion  string            `yaml:"templateVersion,omitempty"`
	Input            *SchemaRef        `yaml:"input,omitempty"`
	Output           *SchemaRef        `yaml:"output,omitempty"`
	Env              []EnvVar          `yaml:"env,omitempty"`
}

func (s FunctionSpec) Method() string {
	if s.Type != "" {
		return s.Type
	}
	return s.HTTPMethod
}

func (s FunctionSpec) NormalizedMethod() string {
	return NormalizeFunctionMethod(s.Method())
}

func NormalizeFunctionMethod(method string) string {
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		return "POST"
	}
	return method
}

func IsSupportedFunctionMethod(method string) bool {
	switch NormalizeFunctionMethod(method) {
	case "GET", "POST", "PUT", "DELETE":
		return true
	default:
		return false
	}
}

type SchemaRef struct {
	Schema string `yaml:"schema,omitempty"`
}

type EnvVar struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value,omitempty"`
	Ref   string `yaml:"ref,omitempty"`
}

// -----------------------------------------------------------------------
// Component  (components/<name>/component.yaml or
//             sources/<name>/source.yaml etc.)
// -----------------------------------------------------------------------

type Component struct {
	APIVersion string        `yaml:"apiVersion"`
	Kind       string        `yaml:"kind"`
	Metadata   ObjectMeta    `yaml:"metadata"`
	Spec       ComponentSpec `yaml:"spec"`
}

type ComponentSpec struct {
	Type             string         `yaml:"type,omitempty"`       // source | destination | transformation
	SourceType       string         `yaml:"sourceType,omitempty"` // STANDARD | EXTRACTOR | KAFKA
	Runtime          string         `yaml:"runtime,omitempty"`
	Language         string         `yaml:"language,omitempty"` // javascript | go | python
	Description      string         `yaml:"description,omitempty"`
	Config           map[string]any `yaml:"config,omitempty"`
	APIAccessRef     string         `yaml:"apiAccessRef,omitempty"`
	SignatureVersion string         `yaml:"signatureVersion,omitempty"`
	TemplateVersion  string         `yaml:"templateVersion,omitempty"`
	SwitchLabels     []string       `yaml:"switchLabels,omitempty"`
	AppType          string         `yaml:"appType,omitempty"`
	Entrypoint       string         `yaml:"entrypoint,omitempty"`
	Build            *BuildSpec     `yaml:"build,omitempty"`
	Input            *SchemaRef     `yaml:"input,omitempty"`
	Output           *SchemaRef     `yaml:"output,omitempty"`
}

type BuildSpec struct {
	Command string `yaml:"command,omitempty"`
	OutDir  string `yaml:"outDir,omitempty"`
	OutFile string `yaml:"outFile,omitempty"`
}

// -----------------------------------------------------------------------
// Environment  (environments/<name>.yaml)
// -----------------------------------------------------------------------

type Environment struct {
	APIVersion string          `yaml:"apiVersion"`
	Kind       string          `yaml:"kind"`
	Metadata   ObjectMeta      `yaml:"metadata"`
	Spec       EnvironmentSpec `yaml:"spec"`
}

// -----------------------------------------------------------------------
// Workspace resource artifacts
// -----------------------------------------------------------------------

type GlobalVariable struct {
	APIVersion string             `yaml:"apiVersion"`
	Kind       string             `yaml:"kind"`
	Metadata   ObjectMeta         `yaml:"metadata"`
	Spec       GlobalVariableSpec `yaml:"spec"`
}

type GlobalVariableSpec struct {
	Key         string `yaml:"key"`
	Value       string `yaml:"value,omitempty"`
	Ref         string `yaml:"ref,omitempty"`
	WorkspaceID string `yaml:"workspaceId,omitempty"`
}

type Configuration struct {
	APIVersion string            `yaml:"apiVersion"`
	Kind       string            `yaml:"kind"`
	Metadata   ObjectMeta        `yaml:"metadata"`
	Spec       ConfigurationSpec `yaml:"spec"`
}

type ConfigurationSpec struct {
	Type        string         `yaml:"type"`
	Key         string         `yaml:"key,omitempty"`
	Value       string         `yaml:"value,omitempty"`
	Ref         string         `yaml:"ref,omitempty"`
	WorkspaceID string         `yaml:"workspaceId,omitempty"`
	APIAccess   map[string]any `yaml:"apiAccess,omitempty"`
}

type EnvironmentSpec struct {
	Target      EnvironmentTarget    `yaml:"target,omitempty"`
	Variables   map[string]string    `yaml:"variables,omitempty"`
	Connections map[string]ConnRef   `yaml:"connections,omitempty"`
	Secrets     map[string]SecretRef `yaml:"secrets,omitempty"`
}

type EnvironmentTarget struct {
	Workspace string `yaml:"workspace,omitempty"`
}

type ConnRef struct {
	Ref   string `yaml:"ref,omitempty"`
	Value string `yaml:"value,omitempty"`
}

type SecretRef struct {
	Ref   string `yaml:"ref,omitempty"`
	Value string `yaml:"value,omitempty"` // only allowed in local/dev env
}

// -----------------------------------------------------------------------
// Shared
// -----------------------------------------------------------------------

type ObjectMeta struct {
	ID          string            `yaml:"id,omitempty"`
	Name        string            `yaml:"name"`
	Description string            `yaml:"description,omitempty"`
	Slug        string            `yaml:"slug,omitempty"`
	Version     string            `yaml:"version,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty"`
}

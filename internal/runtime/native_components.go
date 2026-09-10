package runtime

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5"  //nolint:gosec // Matches processor support for legacy configured hashes.
	"crypto/sha1" //nolint:gosec // Matches processor support for legacy configured hashes.
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	texttemplate "text/template"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types/ref"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gomarkdown/markdown"
	"github.com/jaytaylor/html2text"
	"github.com/ohler55/ojg/jp"
	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/cnips/cli/internal/artifact"
)

type nativeComponentEvent struct {
	Data   any `json:"data"`
	Output any `json:"output,omitempty"`
}

type nativeResult struct {
	Event    string
	Decision *bool
}

var placeholderRe = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)

const localPulledSecretPlaceholder = "cnips-local-placeholder-secret"

type httpRequestConfig struct {
	Method      string            `json:"method"`
	URL         string            `json:"url"`
	QueryParams map[string]string `json:"query_params"`
	Headers     map[string]string `json:"headers"`
	Payload     string            `json:"payload"`
	Timeout     string            `json:"timeout"`
}

type dataMapperConfig struct {
	Mappings     []dataMapperRule `json:"mappings"`
	PassUnmapped bool             `json:"pass_unmapped"`
}

type dataMapperRule struct {
	Source     string `json:"source"`
	Target     string `json:"target"`
	Default    any    `json:"default,omitempty"`
	DefaultSet bool   `json:"-"`
}

func (r *dataMapperRule) UnmarshalJSON(data []byte) error {
	type alias dataMapperRule
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var rule alias
	if err := json.Unmarshal(data, &rule); err != nil {
		return err
	}
	if defaultValue, exists := raw["default"]; exists {
		rule.DefaultSet = true
		if err := json.Unmarshal(defaultValue, &rule.Default); err != nil {
			return fmt.Errorf("error unmarshaling default value: %w", err)
		}
	}
	*r = dataMapperRule(rule)
	return nil
}

type regexExtractorConfig struct {
	SourceField string `json:"source_field"`
	Pattern     string `json:"pattern"`
	Mode        string `json:"mode"`
	OutputField string `json:"output_field"`
	Replacement string `json:"replacement"`
}

type jsonSchemaValidatorConfig struct {
	Schema       map[string]any `json:"schema"`
	AttachErrors bool           `json:"attach_errors"`
}

type templateConfig struct {
	Template    string `json:"template"`
	OutputField string `json:"output_field"`
	OutputMode  string `json:"output_mode"`
}

type aggregatorConfig struct {
	ArrayPath   string `json:"array_path"`
	Operation   string `json:"operation"`
	Field       string `json:"field"`
	GroupBy     string `json:"group_by"`
	OutputField string `json:"output_field"`
}

type sortConfig struct {
	ArrayPath   string        `json:"array_path"`
	SortKeys    []sortKeyRule `json:"sort_keys"`
	OutputField string        `json:"output_field"`
}

type sortKeyRule struct {
	Field     string `json:"field"`
	Direction string `json:"direction"`
}

type deduplicateConfig struct {
	ArrayPath   string   `json:"array_path"`
	KeyFields   []string `json:"key_fields"`
	Keep        string   `json:"keep"`
	OutputField string   `json:"output_field"`
}

type markupConverterConfig struct {
	Mode        string `json:"mode"`
	SourceField string `json:"source_field"`
	OutputField string `json:"output_field"`
}

type cryptoConfig struct {
	Operation      string `json:"operation"`
	Algorithm      string `json:"algorithm"`
	SourceField    string `json:"source_field"`
	SecretAccessID string `json:"secret_access_id"`
	Secret         string `json:"secret"`
	OutputField    string `json:"output_field"`
	Encoding       string `json:"encoding"`
}

type jwtConfig struct {
	Mode           string         `json:"mode"`
	Algorithm      string         `json:"algorithm"`
	SecretAccessID string         `json:"secret_access_id"`
	Secret         string         `json:"secret"`
	SourceField    string         `json:"source_field"`
	Claims         map[string]any `json:"claims"`
	OutputField    string         `json:"output_field"`
}

type validationError struct {
	Message string `json:"message"`
}

var jsonFilterTargetPattern = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*)\s*\.filter\s*\(`)

func executeProcessorNative(step *artifact.Step, name, payload string, envVars map[string]string) (string, string, error) {
	result, err := runProcessorNative(step, nativeName(name), payload, envVars)
	if err != nil {
		return "", "", err
	}
	nextID := step.Next
	if result.Decision != nil {
		nextID = nativeDecisionNext(step, *result.Decision)
	}
	return result.Event, nextID, nil
}

func runProcessorNative(step *artifact.Step, name, payload string, envVars map[string]string) (nativeResult, error) {
	switch name {
	case "http-request":
		return nativeHTTPRequest(step, payload, envVars)
	case "json-filter":
		return nativeCELJSONFilter(step, payload)
	case "stop-error":
		return nativeStopError(step, payload)
	case "data-mapper":
		return nativeDataMapper(step, payload)
	case "regex-extractor":
		return nativeRegexExtractor(step, payload)
	case "json-schema-validator":
		return nativeJSONSchemaValidator(step, payload)
	case "template":
		return nativeTemplate(step, payload)
	case "sort":
		return nativeSort(step, payload)
	case "aggregator":
		return nativeAggregator(step, payload)
	case "deduplicate":
		return nativeDeduplicate(step, payload)
	case "markup-converter":
		return nativeMarkupConverter(step, payload)
	case "crypto":
		return nativeCrypto(step, payload, envVars)
	case "jwt":
		return nativeJWT(step, payload, envVars)
	default:
		return nativeResult{}, fmt.Errorf("unsupported processor native component %q", name)
	}
}

func nativeHTTPRequest(step *artifact.Step, payload string, envVars map[string]string) (nativeResult, error) {
	var config httpRequestConfig
	if err := decodeNativeConfig(step.With, &config); err != nil {
		return nativeResult{}, fmt.Errorf("error unmarshaling HTTP_REQUEST config: %w", err)
	}
	config.Method = strings.ToUpper(strings.TrimSpace(config.Method))
	if config.Method == "" {
		config.Method = http.MethodGet
	}
	if strings.TrimSpace(config.URL) == "" {
		return nativeResult{}, fmt.Errorf("url is required for HTTP_REQUEST component")
	}
	data, err := parseNativePayload(payload)
	if err != nil {
		return nativeResult{}, err
	}
	body := config.Payload
	if body == "" {
		bodyData, err := json.Marshal(data)
		if err != nil {
			return nativeResult{}, fmt.Errorf("error marshaling HTTP_REQUEST payload: %w", err)
		}
		body = string(bodyData)
	}
	requestURL, err := url.Parse(resolveTemplate(config.URL, map[string]string{"_payload": payload}, envVars))
	if err != nil {
		return nativeResult{}, fmt.Errorf("invalid HTTP_REQUEST url: %w", err)
	}
	query := requestURL.Query()
	for key, value := range config.QueryParams {
		query.Set(key, resolveTemplate(value, map[string]string{"_payload": payload}, envVars))
	}
	requestURL.RawQuery = query.Encode()

	timeout := 30 * time.Second
	if config.Timeout != "" {
		parsedTimeout, err := time.ParseDuration(config.Timeout)
		if err != nil {
			return nativeResult{}, fmt.Errorf("invalid HTTP_REQUEST timeout %q: %w", config.Timeout, err)
		}
		timeout = parsedTimeout
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(config.Method, requestURL.String(), bytes.NewBufferString(body))
	if err != nil {
		return nativeResult{}, err
	}
	for key, value := range config.Headers {
		req.Header.Set(key, resolveTemplate(value, map[string]string{"_payload": payload}, envVars))
	}
	if req.Header.Get("Content-Type") == "" && body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nativeResult{}, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nativeResult{}, err
	}
	if resp.StatusCode >= 400 {
		return nativeResult{}, fmt.Errorf("HTTP_REQUEST completed with status %d: %s", resp.StatusCode, string(respBody))
	}
	output := parseNativeComponentOutput(string(respBody))
	event, err := buildNativeResponse(step, payload, data, mergeNativeOutput(data, output))
	return nativeResult{Event: event}, err
}

func nativeCELJSONFilter(step *artifact.Step, payload string) (nativeResult, error) {
	expression, _ := step.With["expression"].(string)
	if strings.TrimSpace(expression) == "" {
		return nativeResult{}, fmt.Errorf("expression cannot be empty for JSON_FILTER component")
	}
	value, data, err := evaluateCELExpression(payload, expression)
	if err != nil {
		return nativeResult{}, err
	}
	output, err := buildJSONFilterOutput(data, expression, value)
	if err != nil {
		return nativeResult{}, err
	}
	event, err := buildNativeResponse(step, payload, data, output)
	return nativeResult{Event: event}, err
}

func nativeStopError(step *artifact.Step, payload string) (nativeResult, error) {
	condition, _ := step.With["condition"].(string)
	condition = strings.TrimSpace(condition)
	if condition == "" {
		return nativeResult{}, fmt.Errorf("condition is required for STOP_ERROR component")
	}

	value, data, err := evaluateCELExpression(payload, condition)
	if err != nil {
		return nativeResult{}, err
	}
	shouldStop, ok := value.(bool)
	if !ok {
		return nativeResult{}, fmt.Errorf("STOP_ERROR condition must evaluate to a boolean")
	}
	if !shouldStop {
		event, err := buildNativeResponse(step, payload, data, mergeNativeOutput(data, data))
		return nativeResult{Event: event}, err
	}

	message, _ := step.With["message"].(string)
	message = strings.TrimSpace(message)
	if message == "" {
		message = "pipeline stopped by stop-error step"
	}
	rendered, err := renderStopErrorMessage(message, data)
	if err != nil {
		return nativeResult{}, err
	}
	return nativeResult{}, fmt.Errorf("%s", rendered)
}

func renderStopErrorMessage(message string, data any) (string, error) {
	var renderErr error
	rendered := placeholderRe.ReplaceAllStringFunc(message, func(match string) string {
		if renderErr != nil {
			return match
		}
		parts := placeholderRe.FindStringSubmatch(match)
		if len(parts) != 2 {
			return match
		}
		value, err := evaluateCELExpressionOnData(data, strings.TrimSpace(parts[1]))
		if err != nil {
			renderErr = err
			return match
		}
		return fmt.Sprint(value)
	})
	if renderErr != nil {
		return "", fmt.Errorf("error rendering STOP_ERROR message: %w", renderErr)
	}
	return rendered, nil
}

func nativeDataMapper(step *artifact.Step, payload string) (nativeResult, error) {
	var config dataMapperConfig
	if err := decodeNativeConfig(step.With, &config); err != nil {
		return nativeResult{}, fmt.Errorf("error unmarshaling DATA_MAPPER config: %w", err)
	}
	if len(config.Mappings) == 0 {
		return nativeResult{}, fmt.Errorf("at least one mapping is required")
	}
	data, err := parseNativePayload(payload)
	if err != nil {
		return nativeResult{}, err
	}
	output := map[string]any{}
	if config.PassUnmapped {
		if dataMap, ok := data.(map[string]any); ok {
			output = cloneMap(dataMap)
		}
	}
	for _, mapping := range config.Mappings {
		if strings.TrimSpace(mapping.Source) == "" || strings.TrimSpace(mapping.Target) == "" {
			return nativeResult{}, fmt.Errorf("mapping source and target are required")
		}
		value, exists, err := getJSONPathValue(data, mapping.Source)
		if err != nil {
			return nativeResult{}, fmt.Errorf("error reading source %q: %w", mapping.Source, err)
		}
		if !exists {
			if !mapping.DefaultSet {
				continue
			}
			value = mapping.Default
		}
		if err := setMappedValue(output, mapping.Target, value); err != nil {
			return nativeResult{}, fmt.Errorf("error writing target %q: %w", mapping.Target, err)
		}
		if config.PassUnmapped {
			if err := removeMappedSourceValue(output, mapping.Source, mapping.Target); err != nil {
				return nativeResult{}, fmt.Errorf("error removing mapped source %q: %w", mapping.Source, err)
			}
		}
	}
	event, err := buildNativeResponse(step, payload, data, mergeNativeOutput(data, output))
	return nativeResult{Event: event}, err
}

func nativeRegexExtractor(step *artifact.Step, payload string) (nativeResult, error) {
	var config regexExtractorConfig
	if err := decodeNativeConfig(step.With, &config); err != nil {
		return nativeResult{}, fmt.Errorf("error unmarshaling REGEX_EXTRACTOR config: %w", err)
	}
	config.SourceField = strings.TrimSpace(config.SourceField)
	config.Pattern = strings.TrimSpace(config.Pattern)
	config.Mode = strings.ToLower(strings.TrimSpace(config.Mode))
	config.OutputField = strings.TrimSpace(config.OutputField)
	if config.SourceField == "" || config.Pattern == "" {
		return nativeResult{}, fmt.Errorf("source_field and pattern are required for REGEX_EXTRACTOR component")
	}
	compiled, err := regexp.Compile(config.Pattern)
	if err != nil {
		return nativeResult{}, fmt.Errorf("error compiling regex pattern: %w", err)
	}
	data, err := parseNativePayload(payload)
	if err != nil {
		return nativeResult{}, err
	}
	sourceValue, exists, err := getJSONPathValue(data, config.SourceField)
	if err != nil {
		return nativeResult{}, fmt.Errorf("error reading source_field %q: %w", config.SourceField, err)
	}
	if !exists {
		return nativeResult{}, fmt.Errorf("source_field %q not found", config.SourceField)
	}
	sourceText, err := valueToString(sourceValue)
	if err != nil {
		return nativeResult{}, err
	}
	switch config.Mode {
	case "extract_first":
		if config.OutputField == "" {
			return nativeResult{}, fmt.Errorf("output_field is required for extract_first mode")
		}
		result := namedRegexMatch(compiled, compiled.FindStringSubmatch(sourceText))
		return writeNativeField(step, payload, data, config.OutputField, result)
	case "extract_all":
		if config.OutputField == "" {
			return nativeResult{}, fmt.Errorf("output_field is required for extract_all mode")
		}
		matches := compiled.FindAllStringSubmatch(sourceText, -1)
		result := make([]map[string]string, 0, len(matches))
		for _, match := range matches {
			result = append(result, namedRegexMatch(compiled, match))
		}
		return writeNativeField(step, payload, data, config.OutputField, result)
	case "replace":
		if config.OutputField == "" {
			return nativeResult{}, fmt.Errorf("output_field is required for replace mode")
		}
		return writeNativeField(step, payload, data, config.OutputField, compiled.ReplaceAllString(sourceText, config.Replacement))
	case "match_test":
		matched := compiled.MatchString(sourceText)
		event, err := buildNativeResponse(step, payload, data, mergeNativeOutput(data, data))
		return nativeResult{Event: event, Decision: &matched}, err
	default:
		return nativeResult{}, fmt.Errorf("mode must be one of extract_first, extract_all, replace, match_test")
	}
}

func nativeJSONSchemaValidator(step *artifact.Step, payload string) (nativeResult, error) {
	var config jsonSchemaValidatorConfig
	if err := decodeNativeConfig(step.With, &config); err != nil {
		return nativeResult{}, fmt.Errorf("error unmarshaling JSON_SCHEMA_VALIDATOR config: %w", err)
	}
	data, err := parseNativePayload(payload)
	if err != nil {
		valid := false
		return nativeResult{Event: payload, Decision: &valid}, nil
	}
	schemaBytes, err := json.Marshal(config.Schema)
	if err != nil {
		return nativeResult{}, fmt.Errorf("error marshaling JSON schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("schema.json", strings.NewReader(string(schemaBytes))); err != nil {
		return nativeResult{}, fmt.Errorf("error adding schema resource: %w", err)
	}
	schema, err := compiler.Compile("schema.json")
	if err != nil {
		return nativeResult{}, fmt.Errorf("error compiling JSON schema: %w", err)
	}
	valid := true
	output := data
	if err := schema.Validate(data); err != nil {
		valid = false
		validationOutput := map[string]any{
			"valid":  false,
			"errors": []validationError{{Message: jsonSchemaValidationReason(err)}},
		}
		output = validationOutput
		if config.AttachErrors {
			output = attachValidationErrors(data, validationOutput["errors"])
		}
	}
	event, err := buildNativeResponse(step, payload, data, mergeNativeOutput(data, output))
	return nativeResult{Event: event, Decision: &valid}, err
}

func nativeTemplate(step *artifact.Step, payload string) (nativeResult, error) {
	var config templateConfig
	if err := decodeNativeConfig(step.With, &config); err != nil {
		return nativeResult{}, fmt.Errorf("error unmarshaling TEMPLATE config: %w", err)
	}
	config.Template = strings.TrimSpace(config.Template)
	config.OutputField = strings.TrimSpace(config.OutputField)
	config.OutputMode = strings.ToLower(strings.TrimSpace(config.OutputMode))
	if config.OutputMode == "" {
		config.OutputMode = "string"
	}
	if config.Template == "" || config.OutputField == "" {
		return nativeResult{}, fmt.Errorf("template and output_field are required for TEMPLATE component")
	}
	if config.OutputMode != "string" && config.OutputMode != "json" {
		return nativeResult{}, fmt.Errorf("output_mode must be string or json")
	}
	data, err := parseNativePayload(payload)
	if err != nil {
		return nativeResult{}, err
	}
	parsed, err := texttemplate.New("native-template").Parse(config.Template)
	if err != nil {
		return nativeResult{}, fmt.Errorf("error parsing TEMPLATE template: %w", err)
	}
	var rendered strings.Builder
	if err := parsed.Execute(&rendered, data); err != nil {
		return nativeResult{}, fmt.Errorf("error executing TEMPLATE template: %w", err)
	}
	var output any = rendered.String()
	if config.OutputMode == "json" {
		output = map[string]any{"result": rendered.String()}
	}
	return writeNativeField(step, payload, data, config.OutputField, output)
}

func nativeSort(step *artifact.Step, payload string) (nativeResult, error) {
	var config sortConfig
	if err := decodeNativeConfig(step.With, &config); err != nil {
		return nativeResult{}, fmt.Errorf("error unmarshaling SORT config: %w", err)
	}
	config.ArrayPath = strings.TrimSpace(config.ArrayPath)
	config.OutputField = strings.TrimSpace(config.OutputField)
	if config.ArrayPath == "" || config.OutputField == "" || len(config.SortKeys) == 0 {
		return nativeResult{}, fmt.Errorf("array_path, sort_keys, and output_field are required for SORT component")
	}
	for i := range config.SortKeys {
		config.SortKeys[i].Direction = strings.ToLower(strings.TrimSpace(config.SortKeys[i].Direction))
		if config.SortKeys[i].Direction != "asc" && config.SortKeys[i].Direction != "desc" {
			return nativeResult{}, fmt.Errorf("sort key %d direction must be asc or desc", i+1)
		}
	}
	data, items, err := nativeArray(payload, config.ArrayPath)
	if err != nil {
		return nativeResult{}, err
	}
	sorted := append([]any(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool {
		less, decided := compareSortItems(sorted[i], sorted[j], config.SortKeys)
		return decided && less
	})
	return writeNativeField(step, payload, data, config.OutputField, sorted)
}

func nativeAggregator(step *artifact.Step, payload string) (nativeResult, error) {
	var config aggregatorConfig
	if err := decodeNativeConfig(step.With, &config); err != nil {
		return nativeResult{}, fmt.Errorf("error unmarshaling AGGREGATOR config: %w", err)
	}
	config.ArrayPath = strings.TrimSpace(config.ArrayPath)
	config.Operation = strings.ToLower(strings.TrimSpace(config.Operation))
	config.Field = strings.TrimSpace(config.Field)
	config.GroupBy = strings.TrimSpace(config.GroupBy)
	config.OutputField = strings.TrimSpace(config.OutputField)
	if config.ArrayPath == "" || config.Operation == "" || config.OutputField == "" {
		return nativeResult{}, fmt.Errorf("array_path, operation, and output_field are required for AGGREGATOR component")
	}
	data, items, err := nativeArray(payload, config.ArrayPath)
	if err != nil {
		return nativeResult{}, err
	}
	result, err := aggregateItems(items, config)
	if err != nil {
		return nativeResult{}, err
	}
	return writeNativeField(step, payload, data, config.OutputField, result)
}

func nativeDeduplicate(step *artifact.Step, payload string) (nativeResult, error) {
	var config deduplicateConfig
	if err := decodeNativeConfig(step.With, &config); err != nil {
		return nativeResult{}, fmt.Errorf("error unmarshaling DEDUPLICATE config: %w", err)
	}
	config.ArrayPath = strings.TrimSpace(config.ArrayPath)
	config.Keep = strings.ToLower(strings.TrimSpace(config.Keep))
	config.OutputField = strings.TrimSpace(config.OutputField)
	if config.Keep == "" {
		config.Keep = "first"
	}
	if config.ArrayPath == "" || config.OutputField == "" || len(config.KeyFields) == 0 {
		return nativeResult{}, fmt.Errorf("array_path, key_fields, and output_field are required for DEDUPLICATE component")
	}
	if config.Keep != "first" && config.Keep != "last" {
		return nativeResult{}, fmt.Errorf("keep must be first or last")
	}
	data, items, err := nativeArray(payload, config.ArrayPath)
	if err != nil {
		return nativeResult{}, err
	}
	deduped, err := deduplicateItems(items, config)
	if err != nil {
		return nativeResult{}, err
	}
	return writeNativeField(step, payload, data, config.OutputField, deduped)
}

func nativeMarkupConverter(step *artifact.Step, payload string) (nativeResult, error) {
	var config markupConverterConfig
	if err := decodeNativeConfig(step.With, &config); err != nil {
		return nativeResult{}, fmt.Errorf("error unmarshaling MARKUP_CONVERTER config: %w", err)
	}
	config.Mode = strings.ToLower(strings.TrimSpace(config.Mode))
	config.SourceField = strings.TrimSpace(config.SourceField)
	config.OutputField = strings.TrimSpace(config.OutputField)
	if config.SourceField == "" || config.OutputField == "" {
		return nativeResult{}, fmt.Errorf("source_field and output_field are required for MARKUP_CONVERTER component")
	}
	data, err := parseNativePayload(payload)
	if err != nil {
		return nativeResult{}, err
	}
	sourceValue, exists, err := getJSONPathValue(data, config.SourceField)
	if err != nil {
		return nativeResult{}, fmt.Errorf("error reading source_field %q: %w", config.SourceField, err)
	}
	if !exists {
		return nativeResult{}, fmt.Errorf("source_field %q not found", config.SourceField)
	}
	sourceText, ok := sourceValue.(string)
	if !ok {
		return nativeResult{}, fmt.Errorf("source_field %q must resolve to a string", config.SourceField)
	}
	var converted string
	switch config.Mode {
	case "md_to_html":
		converted = string(markdown.ToHTML([]byte(sourceText), nil, nil))
	case "html_to_text":
		converted, err = html2text.FromString(sourceText)
	case "strip_tags":
		converted, err = html2text.FromString(sourceText, html2text.Options{OmitLinks: true, TextOnly: true})
	default:
		return nativeResult{}, fmt.Errorf("mode must be md_to_html, html_to_text, or strip_tags")
	}
	if err != nil {
		return nativeResult{}, err
	}
	return writeNativeField(step, payload, data, config.OutputField, converted)
}

func nativeCrypto(step *artifact.Step, payload string, envVars map[string]string) (nativeResult, error) {
	var config cryptoConfig
	if err := decodeNativeConfig(step.With, &config); err != nil {
		return nativeResult{}, fmt.Errorf("error unmarshaling CRYPTO config: %w", err)
	}
	config.Operation = strings.ToLower(strings.TrimSpace(config.Operation))
	config.Algorithm = strings.ToLower(strings.TrimSpace(config.Algorithm))
	config.Encoding = strings.ToLower(strings.TrimSpace(config.Encoding))
	if config.Encoding == "" {
		config.Encoding = "hex"
	}
	if config.SourceField == "" || config.OutputField == "" || config.Operation == "" {
		return nativeResult{}, fmt.Errorf("operation, source_field, and output_field are required for CRYPTO component")
	}
	data, err := parseNativePayload(payload)
	if err != nil {
		return nativeResult{}, err
	}
	sourceValue, exists, err := getJSONPathValue(data, config.SourceField)
	if err != nil {
		return nativeResult{}, fmt.Errorf("error reading source_field %q: %w", config.SourceField, err)
	}
	if !exists {
		return nativeResult{}, fmt.Errorf("source_field %q not found", config.SourceField)
	}
	sourceBytes, err := nativeValueBytes(sourceValue)
	if err != nil {
		return nativeResult{}, err
	}
	var result string
	switch config.Operation {
	case "hash":
		result, err = computeHash(config.Algorithm, sourceBytes, config.Encoding)
	case "hmac":
		secret, err := resolveLocalSecret(config.Secret, config.SecretAccessID, envVars)
		if err != nil {
			return nativeResult{}, err
		}
		result, err = computeHMAC(config.Algorithm, sourceBytes, []byte(secret), config.Encoding)
	case "base64_encode":
		result = base64.StdEncoding.EncodeToString(sourceBytes)
	case "base64_decode":
		decoded, err := base64.StdEncoding.DecodeString(string(sourceBytes))
		if err != nil {
			return nativeResult{}, fmt.Errorf("error decoding base64 source field: %w", err)
		}
		result = string(decoded)
	default:
		return nativeResult{}, fmt.Errorf("unsupported crypto operation: %s", config.Operation)
	}
	if err != nil {
		return nativeResult{}, err
	}
	return writeNativeField(step, payload, data, config.OutputField, result)
}

func nativeJWT(step *artifact.Step, payload string, envVars map[string]string) (nativeResult, error) {
	var config jwtConfig
	if err := decodeNativeConfig(step.With, &config); err != nil {
		return nativeResult{}, fmt.Errorf("error unmarshaling JWT config: %w", err)
	}
	config.Mode = strings.ToLower(strings.TrimSpace(config.Mode))
	config.Algorithm = strings.ToUpper(strings.TrimSpace(config.Algorithm))
	config.SourceField = strings.TrimSpace(config.SourceField)
	config.OutputField = strings.TrimSpace(config.OutputField)
	if config.OutputField == "" {
		return nativeResult{}, fmt.Errorf("output_field is required for JWT component")
	}
	data, err := parseNativePayload(payload)
	if err != nil {
		return nativeResult{}, err
	}
	switch config.Mode {
	case "decode":
		tokenText, err := readJWTToken(config.SourceField, data)
		if err != nil {
			return nativeResult{}, err
		}
		claims := jwt.MapClaims{}
		_, _, err = jwt.NewParser().ParseUnverified(tokenText, claims)
		if err != nil {
			return nativeResult{}, fmt.Errorf("error decoding JWT: %w", err)
		}
		return writeNativeField(step, payload, data, config.OutputField, map[string]any(claims))
	case "sign":
		claims, err := buildJWTClaims(config.Claims, data)
		if err != nil {
			return nativeResult{}, err
		}
		method := jwtSigningMethod(config.Algorithm)
		if method == nil {
			return nativeResult{}, fmt.Errorf("unsupported JWT algorithm: %s", config.Algorithm)
		}
		secret, err := resolveLocalSecret(config.Secret, config.SecretAccessID, envVars)
		if err != nil {
			return nativeResult{}, err
		}
		key, err := jwtSigningKey(config.Algorithm, secret)
		if err != nil {
			return nativeResult{}, err
		}
		token, err := jwt.NewWithClaims(method, jwt.MapClaims(claims)).SignedString(key)
		if err != nil {
			return nativeResult{}, fmt.Errorf("error signing JWT: %w", err)
		}
		return writeNativeField(step, payload, data, config.OutputField, token)
	case "verify":
		tokenText, err := readJWTToken(config.SourceField, data)
		if err != nil {
			return nativeResult{}, err
		}
		method := jwtSigningMethod(config.Algorithm)
		if method == nil {
			return nativeResult{}, fmt.Errorf("unsupported JWT algorithm: %s", config.Algorithm)
		}
		secret, err := resolveLocalSecret(config.Secret, config.SecretAccessID, envVars)
		if err != nil {
			return nativeResult{}, err
		}
		key, err := jwtVerificationKey(config.Algorithm, secret)
		if err != nil {
			return nativeResult{}, err
		}
		claims := jwt.MapClaims{}
		token, err := jwt.ParseWithClaims(tokenText, claims, func(token *jwt.Token) (any, error) {
			if token.Method.Alg() != method.Alg() {
				return nil, fmt.Errorf("unexpected JWT algorithm: %s", token.Method.Alg())
			}
			return key, nil
		})
		valid := err == nil && token != nil && token.Valid
		output := map[string]any{"valid": valid}
		if valid {
			output["claims"] = map[string]any(claims)
		} else if err != nil {
			output["error"] = err.Error()
		}
		result, writeErr := writeNativeField(step, payload, data, config.OutputField, output)
		result.Decision = &valid
		return result, writeErr
	default:
		return nativeResult{}, fmt.Errorf("mode must be sign, verify, or decode")
	}
}

func decodeNativeConfig(with map[string]any, target any) error {
	if with == nil {
		return fmt.Errorf("native config cannot be nil")
	}
	data, err := json.Marshal(with)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func nativeName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "_", "-")
	switch name {
	case "data-mapper", "datamapper":
		return "data-mapper"
	case "regex-extractor", "regexextractor":
		return "regex-extractor"
	case "json-schema-validator", "jsonschemavalidator":
		return "json-schema-validator"
	case "markup-converter", "markupconverter":
		return "markup-converter"
	}
	return name
}

func nativeDecisionNext(step *artifact.Step, decision bool) string {
	key := "false"
	if decision {
		key = "true"
	}
	if next := step.Branches[key]; next != "" {
		return next
	}
	if branches, ok := step.With["branches"].(map[string]any); ok {
		if next, ok := branches[key].(string); ok {
			return next
		}
	}
	return step.Next
}

func parseNativePayload(payload string) (any, error) {
	var data any
	doc := json.NewDecoder(strings.NewReader(payload))
	doc.UseNumber()
	if err := doc.Decode(&data); err != nil {
		return nil, fmt.Errorf("error parsing event payload as JSON: %w", err)
	}
	return normalizeNumbers(unwrapNativeData(data)), nil
}

func unwrapNativeData(data any) any {
	if payload, ok := data.(map[string]any); ok {
		if output, exists := payload["output"]; exists {
			return unwrapSingleObjectArray(output)
		}
		if data, exists := payload["data"]; exists {
			return unwrapSingleObjectArray(data)
		}
	}
	return unwrapSingleObjectArray(data)
}

func unwrapSingleObjectArray(data any) any {
	items, ok := data.([]any)
	if !ok || len(items) != 1 {
		return data
	}
	if _, ok := items[0].(map[string]any); ok {
		return items[0]
	}
	return data
}

func normalizeNumbers(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized[key] = normalizeNumbers(item)
		}
		return normalized
	case []any:
		normalized := make([]any, len(typed))
		for i, item := range typed {
			normalized[i] = normalizeNumbers(item)
		}
		return normalized
	case json.Number:
		if intValue, err := typed.Int64(); err == nil {
			return intValue
		}
		if floatValue, err := typed.Float64(); err == nil {
			return floatValue
		}
		return typed.String()
	default:
		return value
	}
}

func buildNativeResponse(step *artifact.Step, eventPayload string, data, output any) (string, error) {
	if nativePayloadWrapped(eventPayload) || preserveNativePayload(step.With) {
		event := nativeComponentEvent{Data: data, Output: output}
		body, err := json.Marshal(event)
		if err != nil {
			return "", fmt.Errorf("error marshaling native component event: %w", err)
		}
		return string(body), nil
	}
	body, err := json.Marshal(output)
	if err != nil {
		return "", fmt.Errorf("error marshaling native component output: %w", err)
	}
	return string(body), nil
}

func nativePayloadWrapped(payload string) bool {
	var data map[string]any
	if err := json.Unmarshal([]byte(payload), &data); err != nil {
		return false
	}
	_, hasData := data["data"]
	_, hasOutput := data["output"]
	return hasData && hasOutput
}

func preserveNativePayload(with map[string]any) bool {
	for _, key := range []string{"preserve_incoming_payload", "preserveIncomingPayload", "preserve_payload"} {
		if value, exists := with[key]; exists {
			return boolConfigValue(value)
		}
	}
	return false
}

func boolConfigValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func writeNativeField(step *artifact.Step, payload string, data any, outputField string, outputValue any) (nativeResult, error) {
	outputData, err := buildNativeOutputData(data, outputField, outputValue)
	if err != nil {
		return nativeResult{}, fmt.Errorf("error writing output_field %q: %w", outputField, err)
	}
	event, err := buildNativeResponse(step, payload, data, mergeNativeOutput(data, outputData))
	return nativeResult{Event: event}, err
}

func buildNativeOutputData(data any, outputField string, outputValue any) (any, error) {
	outputData := cloneJSONValue(data)
	outputMap, ok := outputData.(map[string]any)
	if !ok {
		outputMap = map[string]any{"value": outputData}
		outputData = outputMap
	}
	if err := setMappedValue(outputMap, outputField, outputValue); err != nil {
		return nil, err
	}
	return outputData, nil
}

func mergeNativeOutput(data, output any) any {
	outputMap, outputIsMap := output.(map[string]any)
	dataMap, dataIsMap := data.(map[string]any)
	if dataIsMap {
		baseMap := dataMap
		if existingOutput, ok := dataMap["output"].(map[string]any); ok {
			baseMap = existingOutput
		}
		merged := cloneMap(baseMap)
		if outputIsMap {
			overlay := outputMap
			if nestedOutput, ok := outputMap["output"].(map[string]any); ok {
				overlay = nestedOutput
			}
			for key, value := range overlay {
				merged[key] = cloneJSONValue(value)
			}
		} else {
			merged["result"] = cloneJSONValue(output)
		}
		return merged
	}
	if outputIsMap {
		return cloneMap(outputMap)
	}
	return output
}

func cloneMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = cloneJSONValue(value)
	}
	return result
}

func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case map[interface{}]interface{}:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[fmt.Sprint(key)] = cloneJSONValue(item)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = cloneJSONValue(item)
		}
		return result
	default:
		return value
	}
}

func getJSONPathValue(data any, path string) (any, bool, error) {
	expr, err := parseJSONPathExpression(path)
	if err != nil {
		return nil, false, err
	}
	results := expr.Get(data)
	if len(results) == 0 {
		return nil, false, nil
	}
	if len(results) == 1 {
		return results[0], true, nil
	}
	return results, true, nil
}

func setMappedValue(output map[string]any, target string, value any) error {
	expr, err := parseJSONPathExpression(target)
	if err != nil {
		return err
	}
	if !expr.Normal() {
		return fmt.Errorf("target must be a normal object/index path")
	}
	return expr.Set(output, value)
}

func removeMappedSourceValue(output map[string]any, source, target string) error {
	sourcePath := normalizeJSONPath(source)
	targetPath := normalizeJSONPath(target)
	if sourcePath == targetPath || strings.HasPrefix(targetPath, sourcePath+".") || strings.HasPrefix(targetPath, sourcePath+"[") {
		return nil
	}
	expr, err := parseJSONPathExpression(source)
	if err != nil {
		return err
	}
	if !expr.Normal() {
		return nil
	}
	return expr.Del(output)
}

func parseJSONPathExpression(path string) (jp.Expr, error) {
	path = normalizeJSONPath(path)
	if path == "" {
		return nil, fmt.Errorf("path cannot be empty")
	}
	return jp.ParseString(path)
}

func normalizeJSONPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}
	if strings.HasPrefix(path, "@") {
		return "$" + strings.TrimPrefix(path, "@")
	}
	if !strings.HasPrefix(path, "$") {
		return "$." + path
	}
	return path
}

func nativeArray(payload, path string) (any, []any, error) {
	data, err := parseNativePayload(payload)
	if err != nil {
		return nil, nil, err
	}
	value, exists, err := getJSONPathValue(data, path)
	if err != nil {
		return nil, nil, fmt.Errorf("error reading array_path %q: %w", path, err)
	}
	if !exists {
		return nil, nil, fmt.Errorf("array_path %q not found", path)
	}
	items, ok := value.([]any)
	if !ok {
		return nil, nil, fmt.Errorf("array_path %q must resolve to an array", path)
	}
	return data, items, nil
}

func namedRegexMatch(pattern *regexp.Regexp, match []string) map[string]string {
	result := map[string]string{}
	if len(match) == 0 {
		return result
	}
	for index, name := range pattern.SubexpNames() {
		if index == 0 || name == "" || index >= len(match) {
			continue
		}
		result[name] = match[index]
	}
	return result
}

func valueToString(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			return "", fmt.Errorf("error marshaling source field: %w", err)
		}
		return string(data), nil
	}
}

func jsonSchemaValidationReason(err error) string {
	validationErr, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return err.Error()
	}
	leaf := validationErr
	for len(leaf.Causes) > 0 {
		leaf = leaf.Causes[0]
	}
	if leaf.Message != "" {
		return leaf.Message
	}
	if validationErr.Message != "" {
		return validationErr.Message
	}
	return err.Error()
}

func attachValidationErrors(data any, errors any) any {
	output := cloneJSONValue(data)
	payload, ok := output.(map[string]any)
	if !ok {
		return output
	}
	payload["validationErrors"] = errors
	return payload
}

func compareSortItems(left, right any, keys []sortKeyRule) (bool, bool) {
	for _, key := range keys {
		leftValue, _, _ := getJSONPathValue(left, key.Field)
		rightValue, _, _ := getJSONPathValue(right, key.Field)
		comparison := compareSortValues(leftValue, rightValue)
		if comparison == 0 {
			continue
		}
		if key.Direction == "desc" {
			return comparison > 0, true
		}
		return comparison < 0, true
	}
	return false, false
}

func compareSortValues(left, right any) int {
	if leftNumber, leftOK := sortNumber(left); leftOK {
		if rightNumber, rightOK := sortNumber(right); rightOK {
			switch {
			case leftNumber < rightNumber:
				return -1
			case leftNumber > rightNumber:
				return 1
			default:
				return 0
			}
		}
	}
	if leftBool, leftOK := left.(bool); leftOK {
		if rightBool, rightOK := right.(bool); rightOK {
			if leftBool == rightBool {
				return 0
			}
			if !leftBool && rightBool {
				return -1
			}
			return 1
		}
	}
	leftText := fmt.Sprint(left)
	rightText := fmt.Sprint(right)
	switch {
	case leftText < rightText:
		return -1
	case leftText > rightText:
		return 1
	default:
		return 0
	}
}

func sortNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	default:
		return 0, false
	}
}

func aggregateItems(items []any, config aggregatorConfig) (any, error) {
	if config.GroupBy == "" || config.Operation == "group_by" {
		return aggregateUngrouped(items, config)
	}
	grouped := make(map[string][]any)
	for _, item := range items {
		groupValue, _, _ := getJSONPathValue(item, config.GroupBy)
		key := fmt.Sprint(groupValue)
		grouped[key] = append(grouped[key], item)
	}
	result := make(map[string]any, len(grouped))
	ungrouped := config
	ungrouped.GroupBy = ""
	for group, groupItems := range grouped {
		value, err := aggregateUngrouped(groupItems, ungrouped)
		if err != nil {
			return nil, err
		}
		result[group] = value
	}
	return result, nil
}

func aggregateUngrouped(items []any, config aggregatorConfig) (any, error) {
	switch config.Operation {
	case "count":
		return len(items), nil
	case "sum":
		return aggregateNumeric(items, config.Field, func(values []float64) any {
			total := 0.0
			for _, value := range values {
				total += value
			}
			return total
		})
	case "avg":
		return aggregateNumeric(items, config.Field, func(values []float64) any {
			if len(values) == 0 {
				return nil
			}
			total := 0.0
			for _, value := range values {
				total += value
			}
			return total / float64(len(values))
		})
	case "min":
		return aggregateComparable(items, config.Field, func(current, next any) bool {
			return compareSortValues(next, current) < 0
		})
	case "max":
		return aggregateComparable(items, config.Field, func(current, next any) bool {
			return compareSortValues(next, current) > 0
		})
	case "distinct":
		return aggregateDistinct(items, config.Field)
	case "group_by":
		counts := make(map[string]any)
		for _, item := range items {
			groupValue, _, _ := getJSONPathValue(item, config.GroupBy)
			key := fmt.Sprint(groupValue)
			if counts[key] == nil {
				counts[key] = 0
			}
			counts[key] = counts[key].(int) + 1
		}
		return counts, nil
	default:
		return nil, fmt.Errorf("operation must be count, sum, avg, min, max, distinct, or group_by")
	}
}

func aggregateNumeric(items []any, field string, finish func([]float64) any) (any, error) {
	if field == "" {
		return nil, fmt.Errorf("field is required for numeric aggregation")
	}
	values := make([]float64, 0, len(items))
	for _, item := range items {
		value, exists, err := getJSONPathValue(item, field)
		if err != nil {
			return nil, fmt.Errorf("error reading aggregation field %q: %w", field, err)
		}
		if !exists || value == nil {
			continue
		}
		number, ok := sortNumber(value)
		if !ok {
			return nil, fmt.Errorf("field %q must contain numeric values", field)
		}
		values = append(values, number)
	}
	return finish(values), nil
}

func aggregateComparable(items []any, field string, replace func(any, any) bool) (any, error) {
	if field == "" {
		return nil, fmt.Errorf("field is required for comparable aggregation")
	}
	var selected any
	hasSelected := false
	for _, item := range items {
		value, exists, err := getJSONPathValue(item, field)
		if err != nil {
			return nil, fmt.Errorf("error reading aggregation field %q: %w", field, err)
		}
		if !exists || value == nil {
			continue
		}
		if !hasSelected || replace(selected, value) {
			selected = value
			hasSelected = true
		}
	}
	if !hasSelected {
		return nil, nil
	}
	return selected, nil
}

func aggregateDistinct(items []any, field string) (any, error) {
	if field == "" {
		return nil, fmt.Errorf("field is required for distinct aggregation")
	}
	seen := make(map[string]bool)
	result := make([]any, 0)
	for _, item := range items {
		value, exists, err := getJSONPathValue(item, field)
		if err != nil {
			return nil, fmt.Errorf("error reading aggregation field %q: %w", field, err)
		}
		if !exists {
			continue
		}
		keyBytes, err := json.Marshal(value)
		if err != nil {
			keyBytes = []byte(fmt.Sprint(value))
		}
		key := string(keyBytes)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, value)
	}
	return result, nil
}

func deduplicateItems(items []any, config deduplicateConfig) ([]any, error) {
	if config.Keep == "last" {
		lastIndexByKey := make(map[string]int, len(items))
		for index, item := range items {
			key, err := buildDeduplicateKey(item, config.KeyFields)
			if err != nil {
				return nil, err
			}
			lastIndexByKey[key] = index
		}
		result := make([]any, 0, len(lastIndexByKey))
		for index, item := range items {
			key, err := buildDeduplicateKey(item, config.KeyFields)
			if err != nil {
				return nil, err
			}
			if lastIndexByKey[key] == index {
				result = append(result, item)
			}
		}
		return result, nil
	}
	seen := make(map[string]bool, len(items))
	result := make([]any, 0, len(items))
	for _, item := range items {
		key, err := buildDeduplicateKey(item, config.KeyFields)
		if err != nil {
			return nil, err
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, item)
	}
	return result, nil
}

func buildDeduplicateKey(item any, keyFields []string) (string, error) {
	values := make([]any, 0, len(keyFields))
	for _, field := range keyFields {
		value, _, err := getJSONPathValue(item, field)
		if err != nil {
			return "", fmt.Errorf("error reading key field %q: %w", field, err)
		}
		values = append(values, value)
	}
	keyBytes, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("error building deduplicate key: %w", err)
	}
	return string(keyBytes), nil
}

func nativeValueBytes(value any) ([]byte, error) {
	switch typed := value.(type) {
	case string:
		return []byte(typed), nil
	case []byte:
		return typed, nil
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			return nil, fmt.Errorf("error marshaling source field: %w", err)
		}
		return data, nil
	}
}

func computeHash(algorithm string, source []byte, encoding string) (string, error) {
	factory, err := cryptoHashFactory(algorithm)
	if err != nil {
		return "", err
	}
	h := factory()
	h.Write(source)
	return encodeCryptoBytes(h.Sum(nil), encoding)
}

func computeHMAC(algorithm string, source, secret []byte, encoding string) (string, error) {
	factory, err := cryptoHashFactory(algorithm)
	if err != nil {
		return "", err
	}
	mac := hmac.New(factory, secret)
	mac.Write(source)
	return encodeCryptoBytes(mac.Sum(nil), encoding)
}

func cryptoHashFactory(algorithm string) (func() hash.Hash, error) {
	switch strings.ToLower(algorithm) {
	case "sha256":
		return sha256.New, nil
	case "sha1":
		return sha1.New, nil
	case "md5":
		return md5.New, nil
	default:
		return nil, fmt.Errorf("unsupported crypto algorithm: %s", algorithm)
	}
}

func encodeCryptoBytes(data []byte, encoding string) (string, error) {
	switch encoding {
	case "hex":
		return hex.EncodeToString(data), nil
	case "base64":
		return base64.StdEncoding.EncodeToString(data), nil
	default:
		return "", fmt.Errorf("encoding must be hex or base64")
	}
}

func resolveLocalSecret(secret, secretAccessID string, envVars map[string]string) (string, error) {
	secret = strings.TrimSpace(secret)
	secretAccessID = strings.TrimSpace(secretAccessID)
	if secret != "" {
		return secret, nil
	}
	if secretAccessID != "" {
		for _, candidate := range localSecretCandidates(secretAccessID) {
			if value, ok := envVars[candidate]; ok && strings.TrimSpace(value) != "" {
				return value, nil
			}
			if value := os.Getenv(candidate); strings.TrimSpace(value) != "" {
				return value, nil
			}
		}
		if strings.HasPrefix(secretAccessID, "platform://") {
			return localPulledSecretPlaceholder, nil
		}
	}
	return "", fmt.Errorf("local secret is required; set secret or secret_access_id matching an environment variable")
}

func localSecretCandidates(secretAccessID string) []string {
	candidates := []string{secretAccessID}
	if strings.HasPrefix(secretAccessID, "platform://") {
		parts := strings.Split(strings.TrimPrefix(secretAccessID, "platform://"), "/")
		if len(parts) >= 2 {
			key := parts[len(parts)-1]
			candidates = append(candidates, key, envName(key))
			if len(parts) >= 3 {
				step := parts[len(parts)-2]
				candidates = append(candidates, step+"."+key, envName(step+"_"+key))
			}
		}
	}
	return uniqueStrings(candidates)
}

func envName(value string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - ('a' - 'A')
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, value)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func readJWTToken(sourceField string, data any) (string, error) {
	value, exists, err := getJSONPathValue(data, sourceField)
	if err != nil {
		return "", fmt.Errorf("error reading source_field %q: %w", sourceField, err)
	}
	if !exists {
		return "", fmt.Errorf("source_field %q not found", sourceField)
	}
	tokenText, ok := value.(string)
	if !ok || strings.TrimSpace(tokenText) == "" {
		return "", fmt.Errorf("source_field %q must contain a JWT string", sourceField)
	}
	return tokenText, nil
}

func jwtSigningMethod(algorithm string) jwt.SigningMethod {
	switch strings.ToUpper(strings.TrimSpace(algorithm)) {
	case "HS256":
		return jwt.SigningMethodHS256
	case "HS384":
		return jwt.SigningMethodHS384
	case "HS512":
		return jwt.SigningMethodHS512
	case "RS256":
		return jwt.SigningMethodRS256
	case "RS384":
		return jwt.SigningMethodRS384
	case "RS512":
		return jwt.SigningMethodRS512
	default:
		return nil
	}
}

func jwtSigningKey(algorithm, secret string) (any, error) {
	if strings.HasPrefix(strings.ToUpper(algorithm), "HS") {
		return []byte(secret), nil
	}
	privateKey, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(secret))
	if err != nil {
		return nil, fmt.Errorf("error parsing JWT RSA private key: %w", err)
	}
	return privateKey, nil
}

func jwtVerificationKey(algorithm, secret string) (any, error) {
	if strings.HasPrefix(strings.ToUpper(algorithm), "HS") {
		return []byte(secret), nil
	}
	publicKey, err := jwt.ParseRSAPublicKeyFromPEM([]byte(secret))
	if err == nil {
		return publicKey, nil
	}
	privateKey, privateErr := jwt.ParseRSAPrivateKeyFromPEM([]byte(secret))
	if privateErr == nil {
		return &privateKey.PublicKey, nil
	}
	return nil, fmt.Errorf("error parsing JWT RSA key: public key error: %v; private key error: %v", err, privateErr)
}

func buildJWTClaims(configClaims map[string]any, data any) (map[string]any, error) {
	claims := map[string]any{}
	for key, rawValue := range configClaims {
		if key == "exp_in_seconds" {
			seconds, ok := sortNumber(rawValue)
			if !ok {
				return nil, fmt.Errorf("exp_in_seconds must be numeric")
			}
			claims["exp"] = timeNowUnixPlus(int64(seconds))
			continue
		}
		value := rawValue
		if path, ok := rawValue.(string); ok {
			trimmed := strings.TrimSpace(path)
			if strings.HasPrefix(trimmed, "$") || strings.HasPrefix(trimmed, "@") {
				resolved, exists, err := getJSONPathValue(data, trimmed)
				if err != nil {
					return nil, fmt.Errorf("error resolving JWT claim %q from %q: %w", key, trimmed, err)
				}
				if !exists {
					return nil, fmt.Errorf("JWT claim %q source %q not found", key, trimmed)
				}
				value = resolved
			}
		}
		claims[key] = value
	}
	return claims, nil
}

func timeNowUnixPlus(seconds int64) int64 {
	return time.Now().Add(time.Duration(seconds) * time.Second).Unix()
}

func evaluateCELExpression(payload, expression string) (any, any, error) {
	data, err := parseNativePayload(payload)
	if err != nil {
		return nil, nil, err
	}
	value, err := evaluateCELExpressionOnData(data, expression)
	if err != nil {
		return nil, nil, err
	}
	return value, data, nil
}

func evaluateCELExpressionOnData(data any, expression string) (any, error) {
	var envOpts []cel.EnvOption
	if eventMap, ok := data.(map[string]any); ok {
		for key := range eventMap {
			envOpts = append(envOpts, cel.Variable(key, cel.DynType))
		}
	}
	env, err := cel.NewEnv(envOpts...)
	if err != nil {
		return nil, fmt.Errorf("error creating CEL environment: %w", err)
	}
	ast, issues := env.Parse(expression)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("error parsing CEL expression: %w", issues.Err())
	}
	checked, issues := env.Check(ast)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("error type-checking CEL expression: %w", issues.Err())
	}
	program, err := env.Program(checked)
	if err != nil {
		return nil, fmt.Errorf("error creating CEL program: %w", err)
	}
	result, _, err := program.Eval(data)
	if err != nil {
		return nil, fmt.Errorf("error evaluating CEL expression: %w", err)
	}
	return convertCELResultToNative(result), nil
}

func convertCELResultToNative(result ref.Val) any {
	if nativeValue, err := result.ConvertToNative(reflect.TypeOf([]any{})); err == nil {
		return jsonSafeValue(nativeValue)
	}
	if nativeValue, err := result.ConvertToNative(reflect.TypeOf(map[string]any{})); err == nil {
		return jsonSafeValue(nativeValue)
	}
	if nativeValue, err := result.ConvertToNative(reflect.TypeOf((*any)(nil)).Elem()); err == nil {
		return jsonSafeValue(nativeValue)
	}
	return jsonSafeValue(result.Value())
}

func jsonSafeValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = jsonSafeValue(item)
		}
		return result
	case map[interface{}]interface{}:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[fmt.Sprint(key)] = jsonSafeValue(item)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = jsonSafeValue(item)
		}
		return result
	default:
		return value
	}
}

func buildJSONFilterOutput(data any, expression string, result any) (any, error) {
	filterPath := jsonFilterTargetPath(expression)
	if filterPath == "" {
		return result, nil
	}
	outputData := cloneJSONValue(data)
	outputMap, ok := outputData.(map[string]any)
	if !ok {
		return result, nil
	}
	if err := setMappedValue(outputMap, filterPath, result); err != nil {
		return nil, fmt.Errorf("error writing JSON_FILTER result to %q: %w", filterPath, err)
	}
	return outputData, nil
}

func parseNativeComponentOutput(output string) any {
	var parsed any
	doc := json.NewDecoder(strings.NewReader(output))
	doc.UseNumber()
	if err := doc.Decode(&parsed); err != nil {
		return output
	}
	return normalizeNumbers(parsed)
}

func jsonFilterTargetPath(expression string) string {
	matches := jsonFilterTargetPattern.FindStringSubmatch(strings.TrimSpace(expression))
	if len(matches) != 2 {
		return ""
	}
	return matches[1]
}

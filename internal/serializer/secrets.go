package serializer

import (
	"fmt"
	"strings"
)

func sanitizePulledMap(value map[string]any, baseRef string) map[string]any {
	if len(value) == 0 {
		return value
	}
	out := make(map[string]any, len(value))
	for key, raw := range value {
		out[key] = sanitizePulledValue(key, raw, baseRef+"/"+slugPart(key))
	}
	return out
}

func sanitizePulledValue(key string, value any, ref string) any {
	switch typed := value.(type) {
	case map[string]any:
		return sanitizePulledMap(typed, ref)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = sanitizePulledValue(key, item, fmt.Sprintf("%s/%d", ref, i))
		}
		return out
	case string:
		if pulledSecretField(key) && strings.TrimSpace(typed) != "" && !safePulledReference(typed) {
			return ref
		}
		return typed
	default:
		if pulledSecretField(key) && typed != nil {
			return ref
		}
		return typed
	}
}

func pulledSecretField(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
	if normalized == "" {
		return false
	}
	for _, safe := range []string{"placeholder", "placement", "location", "method", "type", "url", "wellknown", "scope"} {
		if strings.Contains(normalized, safe) {
			return false
		}
	}
	for _, needle := range []string{"password", "passwd", "secret", "token", "api_key", "apikey", "access_key", "private_key", "client_secret"} {
		if strings.Contains(normalized, needle) {
			return true
		}
	}
	return false
}

func safePulledReference(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "${{") ||
		strings.HasPrefix(value, "$") ||
		strings.HasPrefix(value, "env:") ||
		strings.HasPrefix(value, "platform://") ||
		strings.HasPrefix(value, "secret://") ||
		strings.HasPrefix(value, "ref:")
}

func slugPart(value string) string {
	slug := Slug(value)
	if slug == "" {
		return "value"
	}
	return slug
}

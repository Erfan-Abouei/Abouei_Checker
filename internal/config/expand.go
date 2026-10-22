package config

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type expander struct {
	lookup  func(string) (string, bool)
	errs    []FieldError
	secrets map[string]struct{}
	// visited prevents re-expanding a shared YAML node reached through an
	// alias, which could otherwise treat a resolved value as a new reference.
	visited map[*yaml.Node]struct{}
}

func newExpander(lookup func(string) (string, bool)) *expander {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	return &expander{
		lookup:  lookup,
		secrets: make(map[string]struct{}),
		visited: make(map[*yaml.Node]struct{}),
	}
}

func (e *expander) expandConfig(raw *rawConfig) {
	if raw.Storage != nil {
		e.stringValue("storage.database_path", raw.Storage.DatabasePath)
	}
	if raw.Runtime != nil {
		e.durationValue("runtime.shutdown_timeout", raw.Runtime.ShutdownTimeout)
	}
	if raw.Logging != nil {
		e.stringValue("logging.level", raw.Logging.Level)
		e.stringValue("logging.format", raw.Logging.Format)
	}
	e.policy("defaults", raw.Defaults)
	e.notifications("notifications", raw.Notifications)
	if raw.Retention != nil {
		e.durationValue("retention.observations", raw.Retention.Observations)
		e.durationValue("retention.incidents", raw.Retention.Incidents)
		e.durationValue("retention.delivered_events", raw.Retention.DeliveredEvents)
	}
	for i := range raw.Backends {
		prefix := fmt.Sprintf("backends[%d]", i)
		backend := &raw.Backends[i]
		e.stringValue(prefix+".id", backend.ID)
		e.stringValue(prefix+".name", backend.Name)
		e.stringValue(prefix+".url", backend.URL)
		e.headerMap(prefix+".headers", backend.Headers)
		e.policy(prefix, &backend.Policy)
	}
}

func (e *expander) policy(prefix string, policy *rawPolicy) {
	if policy == nil {
		return
	}
	e.durationValue(prefix+".interval", policy.Interval)
	e.durationValue(prefix+".timeout", policy.Timeout)
	if policy.Notifications != nil {
		e.durationValue(prefix+".notifications.reminder_interval", policy.Notifications.ReminderInterval)
	}
	e.bodyMatch(prefix+".body_match", &policy.BodyMatch)
}

func (e *expander) notifications(prefix string, notifications *rawNotifications) {
	if notifications == nil {
		return
	}
	if notifications.Webhook != nil {
		e.stringValue(prefix+".webhook.url", notifications.Webhook.URL)
		e.durationValue(prefix+".webhook.timeout", notifications.Webhook.Timeout)
		e.headerMap(prefix+".webhook.headers", notifications.Webhook.Headers)
	}
	if notifications.Retry != nil {
		e.durationValue(prefix+".retry.initial_backoff", notifications.Retry.InitialBackoff)
		e.durationValue(prefix+".retry.max_backoff", notifications.Retry.MaxBackoff)
	}
}

func (e *expander) stringValue(field string, value *string) {
	if value == nil {
		return
	}
	*value = e.expand(field, *value)
}

func (e *expander) durationValue(field string, value *rawDuration) {
	if value == nil {
		return
	}
	value.text = e.expand(field, value.text)
}

func (e *expander) headerMap(field string, headers map[string]string) {
	if len(headers) == 0 {
		return
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		headers[name] = e.expand(field+"."+name, headers[name])
	}
}

func (e *expander) bodyMatch(field string, node *yaml.Node) {
	node = resolveAlias(node)
	if node == nil || node.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		value := node.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.Value != "value" {
			continue
		}
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
			continue
		}
		if _, seen := e.visited[value]; seen {
			continue
		}
		e.visited[value] = struct{}{}
		value.Value = e.expand(field+".value", value.Value)
	}
}

// expand resolves ${NAME} references and $$ escapes in one pass. Resolved
// values are never rescanned; an environment value containing "$" cannot
// introduce another reference.
func (e *expander) expand(field, value string) string {
	if !strings.Contains(value, "$") {
		return value
	}

	var builder strings.Builder
	builder.Grow(len(value))

	for i := 0; i < len(value); {
		if value[i] != '$' {
			builder.WriteByte(value[i])
			i++
			continue
		}

		if i+1 < len(value) && value[i+1] == '$' {
			builder.WriteByte('$')
			i += 2
			continue
		}

		if i+1 >= len(value) || value[i+1] != '{' {
			builder.WriteByte('$')
			i++
			continue
		}

		end := strings.IndexByte(value[i+2:], '}')
		if end < 0 {
			e.add(field, "malformed environment reference: missing closing brace")
			return ""
		}
		name := value[i+2 : i+2+end]
		if !envNamePattern.MatchString(name) {
			e.add(field, "malformed environment reference (expected ${NAME})")
			return ""
		}

		resolved, ok := e.lookup(name)
		if !ok {
			e.add(field, "environment variable %s is not set", name)
			return ""
		}
		if resolved == "" {
			e.add(field, "environment variable %s is empty", name)
			return ""
		}

		builder.WriteString(resolved)
		e.secrets[resolved] = struct{}{}
		i += 2 + end + 1
	}

	return builder.String()
}

func (e *expander) add(field, format string, args ...any) {
	e.errs = append(e.errs, FieldError{
		Field:   field,
		Message: fmt.Sprintf(format, args...),
	})
}

func (e *expander) secretValues() []string {
	values := make([]string, 0, len(e.secrets))
	for value := range e.secrets {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func resolveAlias(node *yaml.Node) *yaml.Node {
	const maxAliasDepth = 100
	for depth := 0; node != nil && node.Kind == yaml.AliasNode && node.Alias != nil && depth < maxAliasDepth; depth++ {
		node = node.Alias
	}
	return node
}

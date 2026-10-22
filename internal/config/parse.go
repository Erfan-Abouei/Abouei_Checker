package config

import (
	"bytes"
	"errors"
	"io"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ParseOptions controls parsing of configuration bytes that were already read
// from a file.
type ParseOptions struct {
	// Dir is the directory containing the configuration file. Relative
	// database paths are resolved against it. Empty leaves paths relative.
	Dir string
	// LookupEnv resolves environment references. Nil uses the process
	// environment through os.LookupEnv.
	LookupEnv func(string) (string, bool)
	// Source is recorded on the returned Config for diagnostics.
	Source Source
}

// Parse parses, expands, validates, and normalizes one YAML document. It is a
// pure operation and can be tested independently of the filesystem.
func Parse(data []byte, opts ParseOptions) (*Config, error) {
	if len(data) > MaxFileBytes {
		return nil, validationError("", "configuration file exceeds the 1 MiB limit")
	}
	if err := checkDocument(data); err != nil {
		return nil, err
	}

	var raw rawConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil {
		return nil, &ValidationError{Errors: []FieldError{{
			Message: "invalid configuration: " + sanitizeYAMLError(err),
		}}}
	}

	expander := newExpander(opts.LookupEnv)
	expander.expandConfig(&raw)
	if len(expander.errs) > 0 {
		return nil, &ValidationError{Errors: expander.errs}
	}

	validator := &validator{opts: opts}
	cfg := validator.build(&raw)
	if len(validator.errs) > 0 {
		return nil, &ValidationError{Errors: validator.errs}
	}

	cfg.Source = opts.Source
	cfg.secrets = expander.secretValues()
	return cfg, nil
}

func checkDocument(data []byte) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))

	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		if errors.Is(err, io.EOF) {
			return validationError("", "configuration file is empty")
		}
		return validationError("", "invalid YAML: %s", sanitizeYAMLError(err))
	}

	var extra yaml.Node
	switch err := decoder.Decode(&extra); {
	case err == nil:
		return validationError("", "configuration must contain exactly one YAML document")
	case !errors.Is(err, io.EOF):
		return validationError("", "invalid YAML: %s", sanitizeYAMLError(err))
	}

	if len(document.Content) == 0 {
		return validationError("", "configuration document is empty; expected a mapping")
	}

	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		if root.Tag == "!!null" {
			return validationError("", "configuration document is empty; expected a mapping with version and backends")
		}
		return validationError("", "configuration root must be a mapping")
	}
	return nil
}

var (
	notFoundTypePattern   = regexp.MustCompile(`\bnot found in type \S+`)
	alreadySetTypePattern = regexp.MustCompile(`\balready set in type \S+`)
	internalTypePattern   = regexp.MustCompile(`\bconfig\.raw[A-Za-z0-9_]*\b`)
	listTypePattern       = regexp.MustCompile(`\[\]config\.raw[A-Za-z0-9_]*\b`)
	quotedValueSnippet    = regexp.MustCompile("`[^`]*`")
)

// sanitizeYAMLError removes internal Go type names and raw scalar snippets from
// decoder errors while preserving line numbers. Raw configuration values may
// appear in type-mismatch messages, so they are replaced: resolved environment
// values cannot appear here because expansion runs after decoding.
func sanitizeYAMLError(err error) string {
	var typeErr *yaml.TypeError
	if errors.As(err, &typeErr) {
		messages := make([]string, 0, len(typeErr.Errors))
		for _, message := range typeErr.Errors {
			messages = append(messages, sanitizeYAMLErrorText(message))
		}
		return strings.Join(messages, "; ")
	}
	return sanitizeYAMLErrorText(err.Error())
}

func sanitizeYAMLErrorText(message string) string {
	message = strings.TrimPrefix(message, "yaml: ")
	message = notFoundTypePattern.ReplaceAllString(message, "is not a recognized field")
	message = alreadySetTypePattern.ReplaceAllString(message, "is already set")
	message = listTypePattern.ReplaceAllString(message, "a list value")
	message = internalTypePattern.ReplaceAllString(message, "the expected type")
	message = quotedValueSnippet.ReplaceAllString(message, "the provided value")
	return message
}

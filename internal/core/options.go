package core

import (
	"fmt"
	"strconv"
	"strings"
)

// Options is a case-insensitive --key value store with the semantics of the
// C# CdCommandOptions / CliArguments parsers: options start with --, take
// either --key=value or --key value (the next argument unconditionally), and
// reject duplicates.
type Options struct {
	values map[string]string
}

// ParseOptions parses CD/DVD-style arguments.
func ParseOptions(arguments []string) (Options, error) {
	values := make(map[string]string)
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if !strings.HasPrefix(argument, "--") {
			return Options{}, fmt.Errorf("Unexpected argument '%s'.", argument)
		}
		var key, value string
		if equals := strings.IndexByte(argument, '='); equals >= 0 {
			key = argument[2:equals]
			value = argument[equals+1:]
		} else {
			key = argument[2:]
			index++
			if index >= len(arguments) {
				return Options{}, fmt.Errorf("Missing value for --%s.", key)
			}
			value = arguments[index]
		}
		lower := strings.ToLower(key)
		if _, exists := values[lower]; exists {
			return Options{}, fmt.Errorf("Option --%s was specified more than once.", key)
		}
		values[lower] = value
	}
	return Options{values: values}, nil
}

// Contains reports whether the key was specified.
func (o Options) Contains(key string) bool {
	_, ok := o.values[strings.ToLower(key)]
	return ok
}

// Require returns the value or an error when absent.
func (o Options) Require(key string) (string, error) {
	if value, ok := o.values[strings.ToLower(key)]; ok {
		return value, nil
	}
	return "", fmt.Errorf("Missing required option --%s.", key)
}

// GetString returns the value or the fallback.
func (o Options) GetString(key, fallback string) string {
	if value, ok := o.values[strings.ToLower(key)]; ok {
		return value
	}
	return fallback
}

// GetInt returns the parsed int or the fallback.
func (o Options) GetInt(key string, fallback int) (int, error) {
	value, ok := o.values[strings.ToLower(key)]
	if !ok {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("--%s: %w", key, err)
	}
	return int(parsed), nil
}

// GetLong returns the parsed int64 or the fallback.
func (o Options) GetLong(key string, fallback int64) (int64, error) {
	value, ok := o.values[strings.ToLower(key)]
	if !ok {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("--%s: %w", key, err)
	}
	return parsed, nil
}

// GetDouble returns the parsed float64 or the fallback.
func (o Options) GetDouble(key string, fallback float64) (float64, error) {
	value, ok := o.values[strings.ToLower(key)]
	if !ok {
		return fallback, nil
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0, fmt.Errorf("--%s: %w", key, err)
	}
	return parsed, nil
}

// GetBoolean returns the parsed bool or the fallback. "1"/"0" are accepted
// like the C# original; otherwise the value must be true/false.
func (o Options) GetBoolean(key string, fallback bool) (bool, error) {
	value, ok := o.values[strings.ToLower(key)]
	if !ok {
		return fallback, nil
	}
	switch value {
	case "1":
		return true, nil
	case "0":
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, fmt.Errorf("--%s must be true or false", key)
}

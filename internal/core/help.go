package core

import (
	"fmt"
	"strings"
)

// HelpFlag reports whether the argument asks for help.
func HelpFlag(argument string) bool {
	return argument == "--help" || argument == "-h"
}

// FormatCommandHelp renders the help text for one command of a module.
func FormatCommandHelp(module DiscModuleDescriptor, command DiscCommandDescriptor) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "%s — %s\n\n%s\n\n", command.Name, command.DisplayName, command.Description)
	fmt.Fprintf(&builder, "Usage: disc-image-studio %s [OPTIONS]\n", command.Name)
	if len(command.Options) == 0 {
		builder.WriteString("\nThis command takes no options.\n")
		return builder.String()
	}
	builder.WriteString("\nOptions:\n")
	rendered := make([]string, len(command.Options))
	width := 0
	for i, option := range command.Options {
		rendered[i] = "--" + option.Name + " " + optionPlaceholder(option)
		if len(rendered[i]) > width {
			width = len(rendered[i])
		}
	}
	for i, option := range command.Options {
		fmt.Fprintf(&builder, "  %-*s  %s\n", width, rendered[i], optionAnnotation(option))
	}
	if len(command.Notes) > 0 {
		builder.WriteString("\nNotes:\n")
		for _, note := range command.Notes {
			fmt.Fprintf(&builder, "  %s\n", note)
		}
	}
	return builder.String()
}

func optionPlaceholder(option DiscOptionDefinition) string {
	switch option.ValueType {
	case OptionInputFile, OptionOutputFile:
		return "<file>"
	case OptionInputFolder:
		return "<folder>"
	case OptionInteger:
		return "<int>"
	case OptionDecimal:
		return "<number>"
	case OptionBoolean:
		return "<true|false>"
	case OptionChoice:
		if len(option.Choices) > 0 {
			return "<" + strings.Join(option.Choices, "|") + ">"
		}
		return "<choice>"
	default:
		return "<text>"
	}
}

func optionAnnotation(option DiscOptionDefinition) string {
	annotation := option.DisplayName
	if option.Unit != "" {
		annotation += " [" + option.Unit + "]"
	}
	var parts []string
	if option.DefaultValue != "" {
		parts = append(parts, "default: "+option.DefaultValue)
	}
	if option.Required {
		parts = append(parts, "required")
	}
	if len(parts) > 0 {
		annotation += " (" + strings.Join(parts, ", ") + ")"
	}
	return annotation
}

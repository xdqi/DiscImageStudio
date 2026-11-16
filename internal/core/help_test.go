package core

import (
	"strings"
	"testing"
)

func TestHelpFlag(t *testing.T) {
	for _, flag := range []string{"--help", "-h"} {
		if !HelpFlag(flag) {
			t.Fatalf("%s must be a help flag", flag)
		}
	}
	for _, other := range []string{"help", "--help=x", "-help", "--", ""} {
		if HelpFlag(other) {
			t.Fatalf("%s must not be a help flag", other)
		}
	}
}

func TestFormatCommandHelp(t *testing.T) {
	module := DiscModuleDescriptor{ID: "test", DisplayName: "Test"}
	command := DiscCommandDescriptor{
		Name:        "demo",
		DisplayName: "Demo command",
		Description: "Does demo things.",
		Options: []DiscOptionDefinition{
			Opt("input", "Source image", OptionInputFile, true, "", ""),
			Opt("r0", "Inner radius", OptionDecimal, false, "24.5", "mm"),
			Opt("file-position", "File position", OptionChoice, false, "inner", "", "inner", "outer"),
			Opt("interleave", "Delay interleave", OptionBoolean, false, "true", ""),
			Opt("note", "Free text", OptionString, false, "", ""),
		},
	}
	help := FormatCommandHelp(module, command)
	for _, want := range []string{
		"demo — Demo command",
		"Does demo things.",
		"Usage: disc-image-studio demo [OPTIONS]",
		"--input <file>",
		"Source image (required)",
		"--r0 <number>",
		"Inner radius [mm] (default: 24.5)",
		"--file-position <inner|outer>",
		"File position (default: inner)",
		"--interleave <true|false>",
		"--note <text>",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("help is missing %q:\n%s", want, help)
		}
	}
	if strings.Contains(help, "--note <text>   \n") {
		t.Fatal("options without annotations should not have trailing parens")
	}
}

func TestFormatCommandHelpNoOptions(t *testing.T) {
	command := DiscCommandDescriptor{Name: "selftest", DisplayName: "Self test", Description: "Runs tests."}
	help := FormatCommandHelp(DiscModuleDescriptor{}, command)
	if !strings.Contains(help, "This command takes no options.") {
		t.Fatalf("missing no-options note:\n%s", help)
	}
}

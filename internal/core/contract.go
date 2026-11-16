// Package core holds the shared CLI contracts: module descriptors, job
// request/result types and the module catalog, ported from
// DiscImageStudio.Core (OpticalDiscContracts.cs, DiscModuleCatalog.cs).
package core

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// DiscModuleCapabilities mirrors the C# [Flags] enum.
type DiscModuleCapabilities uint32

const (
	CapNone                    DiscModuleCapabilities = 0
	CapRawTrackGeneration      DiscModuleCapabilities = 1 << 0
	CapDataImageGeneration     DiscModuleCapabilities = 1 << 1
	CapGeometryPreview         DiscModuleCapabilities = 1 << 2
	CapEncodedOutputPreview    DiscModuleCapabilities = 1 << 3
	CapHybridFilesystem        DiscModuleCapabilities = 1 << 4
	CapErrorCorrectionEncoding DiscModuleCapabilities = 1 << 5
)

// DiscOptionValueType mirrors the C# enum.
type DiscOptionValueType int

const (
	OptionString DiscOptionValueType = iota
	OptionInputFile
	OptionOutputFile
	OptionInputFolder
	OptionInteger
	OptionDecimal
	OptionBoolean
	OptionChoice
)

// DiscOptionDefinition describes one named command-line option.
type DiscOptionDefinition struct {
	Name         string
	DisplayName  string
	ValueType    DiscOptionValueType
	Required     bool
	DefaultValue string // empty means "no default"
	Unit         string
	Choices      []string
}

// Opt is a shorthand constructor matching the C# record's argument order.
func Opt(name, displayName string, valueType DiscOptionValueType, required bool, defaultValue, unit string, choices ...string) DiscOptionDefinition {
	return DiscOptionDefinition{
		Name:         name,
		DisplayName:  displayName,
		ValueType:    valueType,
		Required:     required,
		DefaultValue: defaultValue,
		Unit:         unit,
		Choices:      choices,
	}
}

// DiscCommandDescriptor describes one command of a module.
type DiscCommandDescriptor struct {
	Name         string
	DisplayName  string
	Description  string
	Capabilities DiscModuleCapabilities
	Options      []DiscOptionDefinition
	// Notes are extra help-only lines (e.g. options the CLI fixes or rejects);
	// they are not part of the top-level command listing.
	Notes []string
}

// DiscModuleDescriptor describes one optical-disc module.
type DiscModuleDescriptor struct {
	ID                   string
	DisplayName          string
	Description          string
	SupportsCancellation bool
	Commands             []DiscCommandDescriptor
}

// DiscJobRequest is one parsed command line.
type DiscJobRequest struct {
	Command    string
	Args       []string
	OutputPath string
}

// NewDiscJobRequest validates and normalizes a request.
func NewDiscJobRequest(command string, args []string) (DiscJobRequest, error) {
	if strings.TrimSpace(command) == "" {
		return DiscJobRequest{}, fmt.Errorf("a disc job command is required")
	}
	return DiscJobRequest{Command: strings.ToLower(strings.TrimSpace(command)), Args: args}, nil
}

// DiscJobProgress is one progress report.
type DiscJobProgress struct {
	ModuleID       string
	Stage          string
	Message        string
	Fraction       float64
	HasFraction    bool
	CompletedUnits int64
	TotalUnits     int64
	HasUnits       bool
}

// DiscJobResult is the outcome of a module run.
type DiscJobResult struct {
	ModuleID   string
	Command    string
	ExitCode   int
	Summary    string
	OutputPath string
}

// Succeeded reports whether the job exited with code 0.
func (r DiscJobResult) Succeeded() bool { return r.ExitCode == 0 }

// DiscImageResult is a completed file-generation operation.
type DiscImageResult struct {
	OutputPath string
	Summary    string
}

// ProgressFunc receives progress reports.
type ProgressFunc func(DiscJobProgress)

// OpticalDiscModule is the Go analogue of IOpticalDiscModule.
type OpticalDiscModule interface {
	Descriptor() DiscModuleDescriptor
	Execute(ctx context.Context, req DiscJobRequest, progress ProgressFunc) (DiscJobResult, error)
}

// DiscModuleCatalog resolves command names to modules.
type DiscModuleCatalog struct {
	Modules  []OpticalDiscModule
	commands map[string]OpticalDiscModule
}

// NewDiscModuleCatalog validates and indexes the modules.
func NewDiscModuleCatalog(modules []OpticalDiscModule) (*DiscModuleCatalog, error) {
	if len(modules) == 0 {
		return nil, fmt.Errorf("at least one optical-disc module is required")
	}
	commands := make(map[string]OpticalDiscModule)
	ids := make(map[string]bool)
	for _, module := range modules {
		if module == nil {
			return nil, fmt.Errorf("module must not be nil")
		}
		id := strings.ToLower(module.Descriptor().ID)
		if ids[id] {
			return nil, fmt.Errorf("duplicate optical-disc module id '%s'", module.Descriptor().ID)
		}
		ids[id] = true
		for _, command := range module.Descriptor().Commands {
			key := strings.ToLower(command.Name)
			if _, exists := commands[key]; exists {
				return nil, fmt.Errorf("disc command '%s' is registered by more than one module", command.Name)
			}
			commands[key] = module
		}
	}
	return &DiscModuleCatalog{Modules: modules, commands: commands}, nil
}

// Resolve finds the module for a command or returns an error.
func (c *DiscModuleCatalog) Resolve(command string) (OpticalDiscModule, error) {
	if module, ok := c.commands[strings.ToLower(strings.TrimSpace(command))]; ok {
		return module, nil
	}
	return nil, fmt.Errorf("Unknown optical-disc command '%s'. (Parameter 'command')", command)
}

// SortedCommands returns every command name, for help output tests.
func (c *DiscModuleCatalog) SortedCommands() []string {
	names := make([]string, 0, len(c.commands))
	for name := range c.commands {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

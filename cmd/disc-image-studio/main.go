// Command disc-image-studio is the Go port of the DiscImageStudio CLI:
// an experimental Linux and macOS CLI for offline CD, DVD and Blu-ray image
// generation.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"disc-image-studio/internal/bluray"
	"disc-image-studio/internal/caltarget"
	"disc-image-studio/internal/cd"
	"disc-image-studio/internal/core"
	"disc-image-studio/internal/dvdmodule"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	catalog, err := core.NewDiscModuleCatalog([]core.OpticalDiscModule{
		cd.NewModule(),
		dvdmodule.NewModule(),
		bluray.NewModule(bluray.LoadGray8),
		caltarget.NewModule(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if len(args) == 0 || core.HelpFlag(args[0]) || (args[0] == "help" && len(args) == 1) {
		printGeneralHelp(catalog)
		return 0
	}

	return runCommand(catalog, args)
}

func printGeneralHelp(catalog *core.DiscModuleCatalog) {
	fmt.Println("Disc Image Studio | experimental Linux and macOS CLI for offline CD, DVD and Blu-ray image generation")
	fmt.Println("Usage: disc-image-studio COMMAND [OPTIONS]")
	for _, module := range catalog.Modules {
		descriptor := module.Descriptor()
		fmt.Printf("\n%s\n", descriptor.DisplayName)
		for _, command := range descriptor.Commands {
			fmt.Printf("  %-20s %s\n", command.Name, command.Description)
		}
	}
	fmt.Println("\nRun 'disc-image-studio COMMAND --help' for command options.")
}

// commandHelp prints the help of one command; the command resolves exactly
// like execution does.
func commandHelp(catalog *core.DiscModuleCatalog, args []string) int {
	if len(args) == 0 {
		printGeneralHelp(catalog)
		return 0
	}
	module, err := catalog.Resolve(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	commandName := strings.ToLower(strings.TrimSpace(args[0]))
	for _, command := range module.Descriptor().Commands {
		if command.Name == commandName {
			fmt.Print(core.FormatCommandHelp(module.Descriptor(), command))
			return 0
		}
	}
	// The catalog resolved the command, so the module must declare it.
	fmt.Fprintf(os.Stderr, "error: module '%s' does not declare command '%s'\n", module.Descriptor().ID, commandName)
	return 1
}

func runCommand(catalog *core.DiscModuleCatalog, args []string) int {
	if args[0] == "help" {
		return commandHelp(catalog, args[1:])
	}
	request, err := core.NewDiscJobRequest(args[0], args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	module, err := catalog.Resolve(request.Command)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	for _, argument := range request.Args {
		if core.HelpFlag(argument) {
			return commandHelp(catalog, []string{request.Command})
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		for range signals {
			if !module.Descriptor().SupportsCancellation {
				// Mirror the C# default: without cooperative cancellation the
				// process is terminated immediately (130 = SIGINT exit).
				os.Exit(130)
			}
			cancel()
		}
	}()

	progress := func(value core.DiscJobProgress) {
		if value.Message != "" {
			fmt.Println(value.Message)
		}
	}
	result, err := module.Execute(ctx, request, progress)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "Cancelled.")
			return 130
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if result.Summary != "" {
		fmt.Println(result.Summary)
	}
	return result.ExitCode
}

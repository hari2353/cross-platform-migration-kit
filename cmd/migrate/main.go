package main

import (
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/haris/cross-platform-migration-kit/internal/migration"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("command required: keygen, capture, inspect, restore, resume, verify, doctor")
	}
	switch args[0] {
	case "keygen":
		return keygen(args[1:])
	case "capture":
		return capture(args[1:])
	case "inspect":
		return inspect(args[1:])
	case "restore":
		return restore(args[1:])
	case "verify":
		return verify(args[1:])
	case "resume":
		return resume(args[1:])
	case "doctor":
		return doctor(args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func keygen(args []string) error {
	flags := flag.NewFlagSet("keygen", flag.ContinueOnError)
	output := flags.String("output", "migration.key", "path for the 32-byte secret key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if _, err := os.Stat(*output); err == nil {
		return fmt.Errorf("refusing to overwrite existing key %s", *output)
	}
	key, err := migration.GenerateKey()
	if err != nil {
		return err
	}
	if err := os.WriteFile(*output, key, 0o600); err != nil {
		return fmt.Errorf("write key: %w", err)
	}
	fmt.Printf("generated key %s (%s)\n", *output, hex.EncodeToString(key[:4]))
	return nil
}

func capture(args []string) error {
	flags := flag.NewFlagSet("capture", flag.ContinueOnError)
	source := flags.String("source", "", "source root; defaults to the current user's home directory")
	repository := flags.String("output", "", "encrypted repository directory on the external drive")
	keyPath := flags.String("key-file", "", "path to the separate 32-byte secret key")
	includeApplications := flags.Bool("apps", false, "capture Windows application intent through winget")
	profile := flags.String("profile", "personal", "validated migration profile")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *repository == "" || *keyPath == "" {
		return errors.New("capture requires --output and --key-file")
	}
	if *profile != "personal" {
		return fmt.Errorf("unsupported profile %q", *profile)
	}
	if *source == "" {
		var err error
		*source, err = os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("find user home: %w", err)
		}
	}
	key, err := migration.ReadKey(*keyPath)
	if err != nil {
		return err
	}
	manifest, err := migration.CaptureWithOptions(*source, *repository, key, *includeApplications, nil)
	if err != nil {
		return err
	}
	fmt.Printf("captured %d files, %d exclusions, and %d application intents in %s\n", len(manifest.Items), len(manifest.Exclusions), len(manifest.Applications), *repository)
	return nil
}

func inspect(args []string) error {
	flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
	repository := flags.String("bundle", "", "encrypted repository directory")
	keyPath := flags.String("key-file", "", "path to the separate 32-byte secret key")
	profile := flags.String("profile", "personal", "validated migration profile")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *profile != "personal" {
		return fmt.Errorf("unsupported profile %q", *profile)
	}
	key, err := migration.ReadKey(*keyPath)
	if err != nil {
		return err
	}
	manifest, err := migration.LoadManifest(*repository, key)
	if err != nil {
		return err
	}
	fmt.Printf("source=%s platform=%s files=%d exclusions=%d\n", manifest.SourceRoot, manifest.SourceOS, len(manifest.Items), len(manifest.Exclusions))
	for _, item := range manifest.Items {
		fmt.Printf("%s %d bytes\n", item.RelativePath, item.Size)
	}
	fmt.Printf("application intents=%d\n", len(manifest.Applications))
	for _, application := range manifest.Applications {
		fmt.Printf("app %s:%s %s\n", application.Manager, application.Identifier, application.Version)
	}
	return nil
}

func restore(args []string) error {
	flags := flag.NewFlagSet("restore", flag.ContinueOnError)
	repository := flags.String("bundle", "", "encrypted repository directory")
	destination := flags.String("destination", "", "destination root; defaults to the current user's home directory")
	keyPath := flags.String("key-file", "", "path to the separate 32-byte secret key")
	dryRun := flags.Bool("dry-run", false, "plan without writing files")
	overwrite := flags.Bool("overwrite", false, "overwrite existing destination files")
	installApplications := flags.Bool("install-apps", false, "install captured Windows applications through winget")
	profile := flags.String("profile", "personal", "validated migration profile")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *profile != "personal" {
		return fmt.Errorf("unsupported profile %q", *profile)
	}
	if *destination == "" {
		var err error
		*destination, err = os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("find user home: %w", err)
		}
	}
	if *dryRun {
		report, err := migration.DryRunRestore(*repository, *destination, *keyPath, *profile, *overwrite)
		if err != nil {
			return err
		}
		return migration.WriteHumanReport(os.Stdout, report)
	}
	report, err := migration.ManagedRestore(*repository, *destination, *keyPath, *profile, *overwrite, *installApplications)
	if err != nil {
		return err
	}
	return migration.WriteHumanReport(os.Stdout, report)
}

func verify(args []string) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	repository := flags.String("bundle", "", "encrypted repository directory")
	keyPath := flags.String("key-file", "", "path to the separate 32-byte secret key")
	destination := flags.String("destination", "", "destination root to verify")
	jsonOutput := flags.Bool("json", false, "write JSON report")
	if err := flags.Parse(args); err != nil {
		return err
	}
	key, err := migration.ReadKey(*keyPath)
	if err != nil {
		return err
	}
	if err := migration.Verify(*repository, key); err != nil {
		return err
	}
	if *destination != "" {
		results, err := migration.VerifyDestination(*repository, *destination, key)
		if err != nil {
			return err
		}
		report := migration.NewReport("verify-destination")
		report.Destination = *destination
		for _, result := range results {
			report.Add(result)
		}
		report.Finish()
		if *jsonOutput {
			return migration.WriteJSONReport(os.Stdout, report)
		}
		return migration.WriteHumanReport(os.Stdout, report)
	}
	fmt.Println("repository verification passed")
	return nil
}

func resume(args []string) error {
	flags := flag.NewFlagSet("resume", flag.ContinueOnError)
	repository := flags.String("bundle", "", "encrypted repository directory")
	keyPath := flags.String("key-file", "", "path to the separate 32-byte secret key")
	profile := flags.String("profile", "personal", "validated migration profile")
	installApplications := flags.Bool("install-apps", false, "install captured applications when supported")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *profile != "personal" {
		return fmt.Errorf("unsupported profile %q", *profile)
	}
	report, err := migration.ResumeRestore(*repository, *keyPath, *profile, *installApplications)
	if err != nil {
		return err
	}
	return migration.WriteHumanReport(os.Stdout, report)
}

func doctor(args []string) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	repository := flags.String("bundle", "", "encrypted repository directory")
	keyPath := flags.String("key-file", "", "path to the separate 32-byte secret key")
	destination := flags.String("destination", "", "destination root")
	if err := flags.Parse(args); err != nil {
		return err
	}
	results := migration.Doctor(*repository, *keyPath, *destination)
	report := migration.NewReport("doctor")
	for _, result := range results {
		report.Add(result)
	}
	report.Finish()
	return migration.WriteHumanReport(os.Stdout, report)
}

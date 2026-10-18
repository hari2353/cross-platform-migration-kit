package main

import (
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/haris/cross-platform-migration-kit/internal/migration"
)

// osExit and generateKey are seams: tests override them to drive the
// process-exit path and the key-generation failure without leaving the test
// process.
var (
	osExit      = os.Exit
	generateKey = migration.GenerateKey
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		osExit(1)
	}
}

// userHomeDirFunc is the per-user home resolver; tests override it so the
// default-source and default-destination branches operate on fixtures.
var userHomeDirFunc = os.UserHomeDir

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("command required: keygen, capture, inspect, restore, resume, verify, doctor, pack")
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
	case "pack":
		return pack(args[1:])
	default:
		return fmt.Errorf("unknown command %q (commands: keygen, capture, inspect, restore, resume, verify, doctor, pack)", args[0])
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
	key, err := generateKey()
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
	includeApplications := flags.Bool("apps", false, "capture installed application intents through the detected native package manager (winget/brew/apt/dnf/pacman/zypper/apk/flatpak/snap)")
	profile := flags.String("profile", "personal", "validated migration profile")
	portable := flags.Bool("portable", false, "after capture, write one-click launchers (install.command/.sh/.bat), the drive key, and --binaries executables next to the repository")
	binariesDir := flags.String("binaries", "", "directory with prebuilt destination executables (migrate-darwin-arm64, ...) to copy onto the drive when --portable is set")
	credentials := flags.Bool("credentials", false, "opt-in: also capture plain-text, user-owned credential files (e.g. opencode auth.json) into the encrypted bundle. OS-wrapped secrets (browser password stores, keychains) are never copied regardless of this flag")
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
		*source, err = userHomeDirFunc()
		if err != nil {
			return fmt.Errorf("find user home: %w", err)
		}
	}
	key, err := migration.ReadKey(*keyPath)
	if err != nil {
		return err
	}
	manifest, err := migration.CaptureWithOptions(*source, *repository, key, *includeApplications, *credentials, nil)
	if err != nil {
		return err
	}
	fmt.Printf("captured %d files, %d exclusions, %d application intents, and %d portable settings in %s\n", len(manifest.Items), len(manifest.Exclusions), len(manifest.Applications), len(manifest.Settings), *repository)
	if len(manifest.Checklist) > 0 {
		fmt.Println("finish-setup steps will be shown on the destination (FINISH-SETUP.txt):")
		for _, step := range manifest.Checklist {
			fmt.Printf("  - %s\n", step)
		}
	}
	if *portable {
		return makePortable(*repository, *keyPath, *binariesDir)
	}
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
	installApplications := flags.Bool("install-apps", false, "install captured applications through the destination platform adapter (winget/brew/flatpak/snap/native Linux managers)")
	profile := flags.String("profile", "personal", "validated migration profile")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *profile != "personal" {
		return fmt.Errorf("unsupported profile %q", *profile)
	}
	if *destination == "" {
		var err error
		*destination, err = userHomeDirFunc()
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
		if report.Command != "" {
			if reportErr := migration.WriteHumanReport(os.Stdout, report); reportErr != nil {
				return reportErr
			}
			return fmt.Errorf("restore failed: %w (completed items are safe; run \"migrate resume\" after fixing the problem)", err)
		}
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
	if *destination != "" {
		// The destination plan reads the manifest as well: a corrupt,
		// foreign-key, or mid-copy bundle fails here before any row is
		// printed.
		results, err := migration.VerifyDestination(*repository, *destination, key)
		if err != nil {
			return err
		}
		report := migration.NewReport("verify-destination")
		report.Destination = *destination
		problems := 0
		for _, result := range results {
			if result.Status != "verified" {
				problems++
			}
			report.Add(result)
		}
		report.Finish()
		var writerErr error
		if *jsonOutput {
			writerErr = migration.WriteJSONReport(os.Stdout, report)
		} else {
			writerErr = migration.WriteHumanReport(os.Stdout, report)
		}
		if writerErr != nil {
			return writerErr
		}
		if problems > 0 {
			return fmt.Errorf("destination verification failed: %d of %d files did not verify", problems, len(results))
		}
	}
	// The full repository integrity pass runs regardless of the destination
	// verdict: every object must decrypt to its manifest checksum.
	if err := migration.Verify(*repository, key); err != nil {
		return err
	}
	if *destination == "" {
		fmt.Println("repository verification passed")
	}
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

// makePortable turns the captured repository into a plug-and-play drive: the
// launcher scripts, the encryption key beside the bundle, and, when a binaries
// directory is provided, one static destination executable per platform.
func makePortable(repository, keyPath, binariesDir string) error {
	written, err := migration.PackPortableDrive(repository, keyPath, binariesDir)
	if err != nil {
		return err
	}
	fmt.Println("portable bundle ready on the drive:")
	for _, path := range written {
		fmt.Printf("  %s\n", filepath.Base(path))
	}
	fmt.Println("next: eject the drive, connect it to the new laptop, and open install.command / install.sh / install.bat.")
	return nil
}

func pack(args []string) error {
	flags := flag.NewFlagSet("pack", flag.ContinueOnError)
	repository := flags.String("bundle", "", "captured repository directory")
	keyPath := flags.String("key-file", "", "path to the 32-byte secret key to place on the drive")
	binariesDir := flags.String("binaries", "", "optional directory with prebuilt destination executables")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *repository == "" || *keyPath == "" {
		return errors.New("pack requires --bundle and --key-file")
	}
	return makePortable(*repository, *keyPath, *binariesDir)
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
	blocked := false
	for _, result := range results {
		switch {
		case result.Kind == "destination":
			// Destination findings are advisory: doctor should run before restore.
		case result.Status != "available" && result.Status != "verified":
			blocked = true
		}
		report.Add(result)
	}
	report.Finish()
	if err := migration.WriteHumanReport(os.Stdout, report); err != nil {
		return err
	}
	if blocked {
		return errors.New("doctor found blocking problems: fix the items reported above before migrating")
	}
	return nil
}

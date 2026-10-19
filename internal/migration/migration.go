package migration

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	KeySize       = 32
	manifestFile  = "manifest.enc"
	formatFile    = "format"
	formatVersion = "migration-kit-v1"
	objectDir     = "objects"
	chunkSize     = 4 * 1024 * 1024
)

type Item struct {
	ID           string `json:"id"`
	RelativePath string `json:"path"`
	Size         int64  `json:"size"`
	Mode         uint32 `json:"mode"`
	ModTimeUnix  int64  `json:"mod_time_unix"`
	SHA256       string `json:"sha256"`
}

type Exclusion struct {
	RelativePath string `json:"path"`
	Reason       string `json:"reason"`
}

type Manifest struct {
	Version      int           `json:"version"`
	CreatedAt    time.Time     `json:"created_at"`
	SourceOS     string        `json:"source_os"`
	SourceRoot   string        `json:"source_root_name"`
	Items        []Item        `json:"items"`
	Exclusions   []Exclusion   `json:"exclusions"`
	Applications []Application `json:"applications,omitempty"`
	Settings     []Setting     `json:"settings,omitempty"`
	Checklist    []string      `json:"checklist,omitempty"`
}

type CopyPlan struct {
	Item   Item
	Target string
	Action string
}

func DryRunRestore(repository, destination, key, profile string, overwrite bool) (Report, error) {
	keyBytes, err := ReadKey(key)
	if err != nil {
		return Report{}, err
	}
	manifest, err := LoadManifest(repository, keyBytes)
	if err != nil {
		return Report{}, err
	}
	plans, err := PlanRestore(manifest, destination, overwrite)
	if err != nil {
		return Report{}, err
	}
	state, err := NewRestoreState(manifest, destination, profile, overwrite)
	if err != nil {
		return Report{}, err
	}
	state.DryRunCompleted = true
	if err := SaveRestoreState(repository, keyBytes, state); err != nil {
		return Report{}, err
	}
	report := NewReport("restore-dry-run")
	report.SourceOS = manifest.SourceOS
	report.Destination = destination
	report.Files = len(plans)
	report.Applications = len(manifest.Applications)
	for _, plan := range plans {
		report.Add(Result{Kind: "file", ID: plan.Item.ID, Path: plan.Item.RelativePath, Status: plan.Action})
	}
	for _, plan := range PlanApplications(manifest.SourceOS, goosFunc(), manifest.Applications) {
		report.Add(Result{Kind: "application", ID: plan.Source.Identifier, Status: plan.Status, Detail: plan.Reason})
	}
	report.Finish()
	return report, nil
}

func ManagedRestore(repository, destination, key, profile string, overwrite, installApplications bool) (Report, error) {
	keyBytes, err := ReadKey(key)
	if err != nil {
		return Report{}, err
	}
	return managedRestoreWithKey(repository, destination, keyBytes, profile, overwrite, installApplications)
}

func ResumeRestore(repository, key, profile string, installApplications bool) (Report, error) {
	keyBytes, err := ReadKey(key)
	if err != nil {
		return Report{}, err
	}
	state, err := LoadRestoreState(repository, keyBytes)
	if err != nil {
		return Report{}, fmt.Errorf("load restore state: %w", err)
	}
	return managedRestoreWithKey(repository, state.Destination, keyBytes, profile, state.Overwrite, installApplications)
}

func managedRestoreWithKey(repository, destination string, key []byte, profile string, overwrite, installApplications bool) (Report, error) {
	manifest, err := LoadManifest(repository, key)
	if err != nil {
		return Report{}, err
	}
	plans, err := PlanRestore(manifest, destination, overwrite)
	if err != nil {
		return Report{}, err
	}
	planFingerprint, err := PlanFingerprint(manifest, destination, profile, overwrite)
	if err != nil {
		return Report{}, err
	}
	state, err := LoadRestoreState(repository, key)
	if err != nil {
		return Report{}, errors.New("restore requires a completed dry-run")
	}
	if err := requireMatchingDryRun(state, planFingerprint); err != nil {
		return Report{}, err
	}
	report := NewReport("restore")
	report.SourceOS = manifest.SourceOS
	report.Destination = destination
	report.Files = len(plans)
	report.Applications = len(manifest.Applications)
	for _, plan := range plans {
		if current, ok := state.Items[plan.Item.ID]; ok && current.Status == "copied" {
			actual, hashErr := fileSHA256(plan.Target)
			if hashErr == nil && actual == plan.Item.SHA256 {
				report.Add(Result{Kind: "file", ID: plan.Item.ID, Path: plan.Item.RelativePath, Status: "already-complete"})
				continue
			}
		}
		if plan.Action == "skip-existing" && !overwrite {
			state.Items[plan.Item.ID] = ItemState{Status: "skipped-existing"}
			report.Add(Result{Kind: "file", ID: plan.Item.ID, Path: plan.Item.RelativePath, Status: "skipped-existing"})
			continue
		}
		if err := osMkdirAll(filepath.Dir(plan.Target), 0o700); err != nil {
			state.Items[plan.Item.ID] = ItemState{Status: "failed", Error: err.Error()}
			_ = SaveRestoreState(repository, key, state)
			report.Add(Result{Kind: "file", ID: plan.Item.ID, Path: plan.Item.RelativePath, Status: "failed", Detail: err.Error()})
			return report, err
		}
		objectPath := filepath.Join(repository, objectDir, plan.Item.ID+".bin")
		if err := decryptFile(objectPath, plan.Target, key, plan.Item.ID, plan.Item.SHA256, plan.Item.Size); err != nil {
			state.Items[plan.Item.ID] = ItemState{Status: "failed", Error: err.Error()}
			_ = SaveRestoreState(repository, key, state)
			report.Add(Result{Kind: "file", ID: plan.Item.ID, Path: plan.Item.RelativePath, Status: "failed", Detail: err.Error()})
			return report, err
		}
		if err := osChmod(plan.Target, fs.FileMode(plan.Item.Mode)); err != nil {
			state.Items[plan.Item.ID] = ItemState{Status: "failed", Error: err.Error()}
			_ = SaveRestoreState(repository, key, state)
			report.Add(Result{Kind: "file", ID: plan.Item.ID, Path: plan.Item.RelativePath, Status: "failed", Detail: err.Error()})
			return report, err
		}
		state.Items[plan.Item.ID] = ItemState{Status: "copied"}
		// The copy already succeeded, so the report row is registered before
		// the state save: a failing state write must not hide completed work
		// from the person debugging the restore.
		report.Add(Result{Kind: "file", ID: plan.Item.ID, Path: plan.Item.RelativePath, Status: "copied"})
		if err := SaveRestoreState(repository, key, state); err != nil {
			return report, err
		}
	}
	// Settings restore before application installs: an application manager
	// failing (no network, unsupported package) must never block the
	// identity, SSH, or editor settings a person expects at first login.
	if err := restoreSettings(manifest, destination, &report, overwrite, installApplications, nil); err != nil {
		return report, err
	}
	if installApplications && len(manifest.Applications) > 0 {
		plans := PlanApplications(manifest.SourceOS, goosFunc(), manifest.Applications)
		var installable []Application
		for _, appPlan := range plans {
			if appPlan.Status == "unsupported" {
				report.Add(Result{Kind: "application", ID: appPlan.Source.Identifier, Status: "unsupported", Detail: appPlan.Reason})
				continue
			}
			installable = append(installable, appPlan.Target)
			report.Add(Result{Kind: "application", ID: appPlan.Source.Identifier, Status: "planned", Detail: appPlan.Target.Manager + ":" + appPlan.Target.Identifier})
		}
		if len(installable) > 0 {
			tempDir, tempErr := osMkdirTemp("", "migration-kit-apps-")
			if tempErr != nil {
				return report, tempErr
			}
			if goosFunc() == "windows" && allManager(installable, "winget") {
				installErr := installWindowsApplications(context.Background(), systemCommandRunner{}, installable, filepath.Join(tempDir, "winget-import.json"))
				_ = os.RemoveAll(tempDir)
				status := "installed"
				if installErr != nil {
					status = "failed"
				}
				for _, application := range installable {
					report.Add(Result{Kind: "application", ID: application.Identifier, Status: status, Detail: errorString(installErr)})
				}
				if installErr != nil {
					return report, installErr
				}
			} else {
				firstErr := error(nil)
				_ = os.RemoveAll(tempDir)
				for _, application := range installable {
					if err := InstallApplication(context.Background(), systemCommandRunner{}, application); err != nil {
						report.Add(Result{Kind: "application", ID: application.Identifier, Status: "failed", Detail: err.Error()})
						if firstErr == nil {
							firstErr = err
						}
						continue
					}
					report.Add(Result{Kind: "application", ID: application.Identifier, Status: "installed"})
				}
				if firstErr != nil {
					return report, firstErr
				}
			}
		}
	}
	report.Finish()
	return report, nil
}

// restoreSettings writes the captured portable setting recipes onto the
// destination: Git identity with a platform transform, SSH host config, and
// VS Code settings (plus extension intents when installs are enabled). Each
// recipe reports independently so one unsupported recipe never blocks the rest.
func restoreSettings(manifest Manifest, destination string, report *Report, overwrite, installApplications bool, runner CommandRunner) error {
	for _, setting := range manifest.Settings {
		switch setting.Recipe {
		case "git-v1":
			content, renderErr := RenderGitSetting(setting, manifest.SourceOS, goosFunc())
			if renderErr != nil {
				report.Add(Result{Kind: "setting", ID: setting.Recipe, Status: "unsupported", Detail: renderErr.Error()})
				continue
			}
			path := filepath.Join(destination, ".gitconfig")
			if _, statErr := os.Stat(path); statErr == nil && !overwrite {
				report.Add(Result{Kind: "setting", ID: setting.Recipe, Path: path, Status: "skipped-existing"})
				continue
			}
			if writeErr := os.WriteFile(path, content, 0o600); writeErr != nil {
				return writeErr
			}
			report.Add(Result{Kind: "setting", ID: setting.Recipe, Path: path, Status: "restored"})
		case "ssh-config-v1":
			content, renderErr := RenderSSHConfig(setting)
			if renderErr != nil || content == nil {
				report.Add(Result{Kind: "setting", ID: setting.Recipe, Status: "unsupported", Detail: errorString(renderErr)})
				continue
			}
			sshDir := filepath.Join(destination, ".ssh")
			configPath := filepath.Join(sshDir, "config")
			if _, statErr := os.Stat(configPath); statErr == nil && !overwrite {
				report.Add(Result{Kind: "setting", ID: setting.Recipe, Path: configPath, Status: "skipped-existing"})
				continue
			}
			if err := os.MkdirAll(sshDir, 0o700); err != nil {
				return err
			}
			if writeErr := os.WriteFile(configPath, content, 0o600); writeErr != nil {
				return writeErr
			}
			report.Add(Result{Kind: "setting", ID: setting.Recipe, Path: configPath, Status: "restored"})
		case "vscode-v1":
			content, extensions, renderErr := RenderVSCodeSetting(setting)
			if renderErr != nil {
				report.Add(Result{Kind: "setting", ID: setting.Recipe, Status: "unsupported", Detail: renderErr.Error()})
				continue
			}
			vscodePath, pathErr := vscodeSettingsPath(goosFunc())
			if pathErr != nil {
				report.Add(Result{Kind: "setting", ID: setting.Recipe, Status: "unsupported", Detail: pathErr.Error()})
				continue
			}
			if _, statErr := os.Stat(vscodePath); statErr == nil && !overwrite {
				report.Add(Result{Kind: "setting", ID: setting.Recipe, Path: vscodePath, Status: "skipped-existing"})
				extensions = nil
			} else {
				if err := os.MkdirAll(filepath.Dir(vscodePath), 0o700); err != nil {
					return err
				}
				if writeErr := os.WriteFile(vscodePath, content, 0o644); writeErr != nil {
					return writeErr
				}
				report.Add(Result{Kind: "setting", ID: setting.Recipe, Path: vscodePath, Status: "restored"})
			}
			if installApplications && len(extensions) > 0 {
				installVSCodeExtensions(report, extensions, runner)
			}
		case "chrome-v1":
			bookmarks, extensions, renderErr := RenderChromeSetting(setting)
			if renderErr != nil {
				report.Add(Result{Kind: "setting", ID: setting.Recipe, Status: "unsupported", Detail: renderErr.Error()})
				continue
			}
			userDataDir := chromeUserDataDirForOS(goosFunc(), destination)
			bookmarksPath := filepath.Join(userDataDir, "Default", "Bookmarks")
			if bookmarks != nil {
				if _, statErr := os.Stat(bookmarksPath); statErr == nil && !overwrite {
					report.Add(Result{Kind: "setting", ID: setting.Recipe, Path: bookmarksPath, Status: "skipped-existing"})
				} else {
					if err := os.MkdirAll(filepath.Dir(bookmarksPath), 0o755); err != nil {
						return err
					}
					if writeErr := os.WriteFile(bookmarksPath, bookmarks, 0o644); writeErr != nil {
						return writeErr
					}
					report.Add(Result{Kind: "setting", ID: setting.Recipe, Path: bookmarksPath, Status: "restored"})
				}
			}
			if len(extensions) > 0 {
				// Chrome has no safe headless extension installer; the
				// extension list is informational (Sync reinstalls them).
				report.Add(Result{Kind: "extension", ID: fmt.Sprintf("%d Chrome extensions", len(extensions)), Status: "sync-recommended", Detail: "reinstalled automatically when you sign in to your Google account"})
			}
		case "opencode-v1":
			config, auth, renderErr := RenderOpenCodeSetting(setting)
			if renderErr != nil {
				report.Add(Result{Kind: "setting", ID: setting.Recipe, Status: "unsupported", Detail: renderErr.Error()})
				continue
			}
			if len(config) > 0 {
				configPath := openCodeConfigPath(destination)
				if _, statErr := os.Stat(configPath); statErr == nil && !overwrite {
					report.Add(Result{Kind: "setting", ID: setting.Recipe, Path: configPath, Status: "skipped-existing"})
				} else {
					if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
						return err
					}
					if writeErr := os.WriteFile(configPath, config, 0o644); writeErr != nil {
						return writeErr
					}
					report.Add(Result{Kind: "setting", ID: setting.Recipe, Path: configPath, Status: "restored"})
				}
			}
			if auth != "" {
				authPath := openCodeAuthPath(destination)
				if _, statErr := os.Stat(authPath); statErr == nil && !overwrite {
					report.Add(Result{Kind: "credential", ID: "opencode auth.json", Path: authPath, Status: "skipped-existing"})
				} else {
					if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
						return err
					}
					if writeErr := os.WriteFile(authPath, []byte(auth), 0o600); writeErr != nil {
						return writeErr
					}
					report.Add(Result{Kind: "credential", ID: "opencode auth.json", Path: authPath, Status: "restored"})
				}
			}
		case "terminal-v1", "dev-v1", "docker-v1", "kubernetes-v1":
			if err := restoreCatalogSetting(setting, destination, report, overwrite); err != nil {
				return err
			}
		default:
			report.Add(Result{Kind: "setting", ID: setting.Recipe, Status: "unsupported"})
		}
	}
	if len(manifest.Checklist) > 0 {
		finishPath := filepath.Join(destination, "FINISH-SETUP.txt")
		if _, statErr := os.Stat(finishPath); statErr == nil && !overwrite {
			report.Add(Result{Kind: "checklist", ID: "FINISH-SETUP.txt", Path: finishPath, Status: "skipped-existing"})
		} else {
			content := "Finish these once on this machine to complete the migration:\n\n"
			for index, step := range manifest.Checklist {
				content += fmt.Sprintf("%d. %s\n", index+1, step)
			}
			if err := os.WriteFile(finishPath, []byte(content), 0o644); err != nil {
				return err
			}
			report.Add(Result{Kind: "checklist", ID: "FINISH-SETUP.txt", Path: finishPath, Status: "restored"})
		}
	}
	return nil
}

func allManager(applications []Application, manager string) bool {
	for _, application := range applications {
		if application.Manager != manager {
			return false
		}
	}
	return true
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func GenerateKey() ([]byte, error) {
	key := make([]byte, KeySize)
	// crypto/rand.Read is documented to never return an error on any
	// platform as of Go 1.24, so there is no error branch to carry.
	_, _ = rand.Read(key)
	return key, nil
}

// absPath normalises a path to its absolute form. It is a seam so tests can
// drive the error branches that the operating system never produces in
// practice for ordinary inputs.
var absPath = filepath.Abs

// Fault seams so tests can exercise write-path failures that the operating
// system will not produce on healthy disks. Production resolves these to the
// real file operations.
var (
	fileChmod = func(f *os.File, mode os.FileMode) error { return f.Chmod(mode) }
	fileWrite = func(f *os.File, p []byte) (int, error) { return f.Write(p) }
	fileSync  = func(f *os.File) error { return f.Sync() }
	fileClose = func(f *os.File) error { return f.Close() }
	osChmod   = os.Chmod

	// writeObjectChunk encrypts one cleartext slice into the chunked wire
	// format; tests may smear a disk fault at either chunk write.
	writeObjectChunk = writeChunk
	// encryptObjectFunc seals a single cleartext file into an object blob;
	// tests inject object-encryption failures during capture.
	encryptObjectFunc = encryptFile
	// removePath deletes a path; tests drive the temporary-cleanup failure
	// that healthy disks never produce.
	removePath = os.Remove
	// osMkdirTemp creates the application-inventory workspace; tests inject
	// the workspace-creation failure.
	osMkdirTemp = os.MkdirTemp
	// osMkdirAll creates restore destination directories; tests inject a
	// deterministic destination failure that platform permission models
	// cannot express identically.
	osMkdirAll = os.MkdirAll
	// osCreateTemp creates atomic-write temporaries; tests inject the
	// temporary-creation failure.
	osCreateTemp = os.CreateTemp
)

func ReadKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key: %w", err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("key must be exactly %d bytes", KeySize)
	}
	return key, nil
}

func Capture(sourceRoot, repository string, key []byte) (Manifest, error) {
	return CaptureWithOptions(sourceRoot, repository, key, false, false, systemCommandRunner{})
}

func CaptureWithOptions(sourceRoot, repository string, key []byte, includeApplications, includeCredentials bool, runner CommandRunner) (Manifest, error) {
	if len(key) != KeySize {
		return Manifest{}, errors.New("key must be exactly 32 bytes")
	}
	sourceRoot, err := absPath(sourceRoot)
	if err != nil {
		return Manifest{}, fmt.Errorf("resolve source: %w", err)
	}
	repository, err = absPath(repository)
	if err != nil {
		return Manifest{}, fmt.Errorf("resolve repository: %w", err)
	}
	if isWithin(repository, sourceRoot) {
		return Manifest{}, errors.New("repository cannot be inside source root")
	}
	if err := os.MkdirAll(filepath.Join(repository, objectDir), 0o700); err != nil {
		return Manifest{}, fmt.Errorf("create repository: %w", err)
	}
	if err := os.WriteFile(filepath.Join(repository, formatFile), []byte(formatVersion), 0o600); err != nil {
		return Manifest{}, fmt.Errorf("write repository format: %w", err)
	}
	items, exclusions, err := inventoryWithOptions(sourceRoot, repository, includeCredentials)
	if err != nil {
		return Manifest{}, err
	}
	var applications []Application
	if includeApplications {
		if runner == nil {
			runner = systemCommandRunner{}
		}
		tempDir, err := osMkdirTemp("", "migration-kit-apps-")
		if err != nil {
			return Manifest{}, fmt.Errorf("create application inventory workspace: %w", err)
		}
		defer os.RemoveAll(tempDir)
		inventoried, inventoryErr := inventoryApplications(context.Background(), runner, tempDir)
		if inventoryErr != nil {
			reason := "application inventory unavailable: " + inventoryErr.Error()
			if len(inventoried) > 0 {
				// Partial inventory: keep the intents that were translated
				// and record what could not be read for the final report.
				applications = inventoried
				reason = "application inventory incomplete: " + inventoryErr.Error()
			}
			exclusions = append(exclusions, Exclusion{RelativePath: "applications", Reason: reason})
		} else {
			applications = inventoried
		}
	}
	settings := []Setting{}
	checklist := []string{}
	appendSetting := func(candidate Setting, settingErr error, extraChecklist ...string) {
		if settingErr != nil {
			exclusions = append(exclusions, Exclusion{RelativePath: candidate.Recipe, Reason: "setting capture unavailable: " + settingErr.Error()})
			return
		}
		if len(candidate.Values) > 0 {
			settings = append(settings, candidate)
			checklist = append(checklist, extraChecklist...)
		}
	}
	appendSetting(CaptureGitSetting(sourceRoot))
	appendSetting(CaptureSSHConfig(sourceRoot))
	appendSetting(captureVSCodeSetting(context.Background(), runner))
	chromeSetting, chromeChecklist, chromeErr := captureChromeSetting(sourceRoot)
	appendSetting(chromeSetting, chromeErr, chromeChecklist...)
	opencodeSetting, opencodeChecklist, opencodeErr := captureOpenCodeSetting(sourceRoot, includeCredentials)
	appendSetting(opencodeSetting, opencodeErr, opencodeChecklist...)
	devSettings, devChecklist, devErr := captureDevSettings(sourceRoot)
	if devErr != nil {
		exclusions = append(exclusions, Exclusion{RelativePath: "devenv", Reason: "setting capture unavailable: " + devErr.Error()})
	} else {
		for _, devSetting := range devSettings {
			appendSetting(devSetting, nil)
		}
		if len(devSettings) > 0 {
			checklist = append(checklist, devChecklist...)
		}
	}
	vanished := map[int]bool{}
	for index := range items {
		path := filepath.Join(sourceRoot, filepath.FromSlash(items[index].RelativePath))
		objectPath := filepath.Join(repository, objectDir, items[index].ID+".bin")
		if err := reuseOrEncryptFile(path, objectPath, key, items[index]); err != nil {
			if isVanishedSource(err) {
				// Live sources churn between the walk and the write (sync
				// clients and browsers delete their own files mid-capture).
				// Record the miss instead of aborting a multi-hour capture.
				exclusions = append(exclusions, Exclusion{RelativePath: items[index].RelativePath, Reason: "source vanished during capture"})
				vanished[index] = true
				continue
			}
			return Manifest{}, fmt.Errorf("encrypt %s: %w", items[index].RelativePath, err)
		}
	}
	if len(vanished) > 0 {
		kept := make([]Item, 0, len(items)-len(vanished))
		for index, item := range items {
			if !vanished[index] {
				kept = append(kept, item)
			}
		}
		items = kept
	}
	sort.Slice(exclusions, func(i, j int) bool { return exclusions[i].RelativePath < exclusions[j].RelativePath })
	manifest := Manifest{
		Version:      1,
		CreatedAt:    time.Now().UTC(),
		SourceOS:     runtime.GOOS,
		SourceRoot:   filepath.Base(sourceRoot),
		Items:        items,
		Exclusions:   exclusions,
		Applications: applications,
		Settings:     settings,
		Checklist:    checklist,
	}
	// json.Marshal cannot fail for this primitives-only shape, and
	// encryptBytes cannot fail for a validated 32-byte key.
	encoded, _ := json.Marshal(manifest)
	ciphertext, _ := encryptBytes(key, encoded, []byte("manifest-v1"))
	if err := os.WriteFile(filepath.Join(repository, manifestFile), ciphertext, 0o600); err != nil {
		return Manifest{}, fmt.Errorf("write manifest: %w", err)
	}
	return manifest, nil
}

func LoadManifest(repository string, key []byte) (Manifest, error) {
	if len(key) != KeySize {
		return Manifest{}, errors.New("key must be exactly 32 bytes")
	}
	contents, err := os.ReadFile(filepath.Join(repository, manifestFile))
	if err != nil {
		return Manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	plaintext, err := decryptBytes(key, contents, []byte("manifest-v1"))
	if err != nil {
		return Manifest{}, fmt.Errorf("decrypt manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(plaintext, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if manifest.Version != 1 {
		return Manifest{}, fmt.Errorf("unsupported manifest version %d", manifest.Version)
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func validateManifest(manifest Manifest) error {
	seenPaths := map[string]bool{}
	seenIDs := map[string]bool{}
	for _, item := range manifest.Items {
		if item.RelativePath == "" || filepath.IsAbs(filepath.FromSlash(item.RelativePath)) || strings.HasPrefix(filepath.ToSlash(item.RelativePath), "../") || item.RelativePath == ".." {
			return fmt.Errorf("invalid manifest path %q", item.RelativePath)
		}
		if len(item.ID) != 64 || !isHex(item.ID) || seenIDs[item.ID] {
			return fmt.Errorf("invalid or duplicate manifest object id for %q", item.RelativePath)
		}
		if len(item.SHA256) != 64 || !isHex(item.SHA256) || item.Size < 0 || seenPaths[item.RelativePath] {
			return fmt.Errorf("invalid or duplicate manifest item %q", item.RelativePath)
		}
		seenPaths[item.RelativePath] = true
		seenIDs[item.ID] = true
	}
	return nil
}

func isHex(value string) bool {
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}

func PlanRestore(manifest Manifest, destination string, overwrite bool) ([]CopyPlan, error) {
	destination, err := absPath(destination)
	if err != nil {
		return nil, fmt.Errorf("resolve destination: %w", err)
	}
	plans := make([]CopyPlan, 0, len(manifest.Items))
	for _, item := range manifest.Items {
		target := filepath.Join(destination, filepath.FromSlash(item.RelativePath))
		if !isWithin(target, destination) {
			return nil, fmt.Errorf("item escapes destination: %s", item.RelativePath)
		}
		if err := rejectSymlinkedParents(target, destination); err != nil {
			return nil, fmt.Errorf("unsafe destination %s: %w", item.RelativePath, err)
		}
		action := "copy"
		if _, err := os.Lstat(target); err == nil && !overwrite {
			action = "skip-existing"
		}
		// Note: a non-not-exist Lstat failure cannot be observed here —
		// rejectSymlinkedParents above already Lstats this exact target and
		// forwards any such error, so an inspect branch would be dead code.
		plans = append(plans, CopyPlan{Item: item, Target: target, Action: action})
	}
	return plans, nil
}

func Restore(repository, destination string, key []byte, overwrite, dryRun bool) error {
	return RestoreWithOptions(repository, destination, key, overwrite, dryRun, false, systemCommandRunner{})
}

func RestoreWithOptions(repository, destination string, key []byte, overwrite, dryRun, installApplications bool, runner CommandRunner) error {
	manifest, err := LoadManifest(repository, key)
	if err != nil {
		return err
	}
	plans, err := PlanRestore(manifest, destination, overwrite)
	if err != nil {
		return err
	}
	if dryRun {
		return nil
	}
	for _, plan := range plans {
		if plan.Action == "skip-existing" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(plan.Target), 0o700); err != nil {
			return fmt.Errorf("create destination directory: %w", err)
		}
		objectPath := filepath.Join(repository, objectDir, plan.Item.ID+".bin")
		if err := decryptFile(objectPath, plan.Target, key, plan.Item.ID, plan.Item.SHA256, plan.Item.Size); err != nil {
			return fmt.Errorf("restore %s: %w", plan.Item.RelativePath, err)
		}
		if err := osChmod(plan.Target, fs.FileMode(plan.Item.Mode)); err != nil {
			return fmt.Errorf("restore permissions %s: %w", plan.Item.RelativePath, err)
		}
	}
	if installApplications && len(manifest.Applications) > 0 {
		if runner == nil {
			runner = systemCommandRunner{}
		}
		tempDir, err := osMkdirTemp("", "migration-kit-apps-")
		if err != nil {
			return fmt.Errorf("create application install workspace: %w", err)
		}
		defer os.RemoveAll(tempDir)
		if err := installWindowsApplications(context.Background(), runner, manifest.Applications, filepath.Join(tempDir, "winget-import.json")); err != nil {
			return err
		}
	}
	return nil
}

func VerifyDestination(repository, destination string, key []byte) ([]Result, error) {
	manifest, err := LoadManifest(repository, key)
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(manifest.Items))
	for _, item := range manifest.Items {
		target := filepath.Join(destination, filepath.FromSlash(item.RelativePath))
		if !isWithin(target, destination) {
			return nil, fmt.Errorf("item escapes destination: %s", item.RelativePath)
		}
		actual, err := fileSHA256(target)
		if errors.Is(err, os.ErrNotExist) {
			results = append(results, Result{Kind: "file", ID: item.ID, Path: item.RelativePath, Status: "missing"})
			continue
		}
		if err != nil {
			results = append(results, Result{Kind: "file", ID: item.ID, Path: item.RelativePath, Status: "unreadable", Detail: err.Error()})
			continue
		}
		if actual != item.SHA256 {
			results = append(results, Result{Kind: "file", ID: item.ID, Path: item.RelativePath, Status: "checksum-mismatch"})
			continue
		}
		results = append(results, Result{Kind: "file", ID: item.ID, Path: item.RelativePath, Status: "verified"})
	}
	return results, nil
}

func Verify(repository string, key []byte) error {
	manifest, err := LoadManifest(repository, key)
	if err != nil {
		return err
	}
	for _, item := range manifest.Items {
		objectPath := filepath.Join(repository, objectDir, item.ID+".bin")
		hash, size, err := verifyFile(objectPath, key, item.ID)
		if err != nil {
			return fmt.Errorf("verify %s: %w", item.RelativePath, err)
		}
		if hash != item.SHA256 || size != item.Size {
			return fmt.Errorf("verification mismatch for %s", item.RelativePath)
		}
	}
	return nil
}

func inventory(sourceRoot, repository string) ([]Item, []Exclusion, error) {
	return inventoryWithOptions(sourceRoot, repository, false)
}

// inventoryWithOptions walks the source root. Credential-like paths are always
// excluded; when allowCredentials is set, plain-file application credential
// stores under an "opencode" directory are the single sanctioned exemption
// (owner-approved opt-in). Private keys, OS keychains, and browser password
// stores stay excluded in every mode.
func inventoryWithOptions(sourceRoot, repository string, allowCredentials bool) ([]Item, []Exclusion, error) {
	var items []Item
	var exclusions []Exclusion
	err := filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		outcome, err := classifyWalkEntry(sourceRoot, repository, allowCredentials, path, entry, walkErr)
		if err != nil {
			return err
		}
		if outcome.exclusion != nil {
			exclusions = append(exclusions, *outcome.exclusion)
		}
		if outcome.item != nil {
			items = append(items, *outcome.item)
		}
		if outcome.skipDir {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("walk source: %w", err)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].RelativePath < items[j].RelativePath })
	sort.Slice(exclusions, func(i, j int) bool { return exclusions[i].RelativePath < exclusions[j].RelativePath })
	return items, exclusions, nil
}

// walkOutcome is the per-entry decision of the protected-path walk.
type walkOutcome struct {
	exclusion *Exclusion
	item      *Item
	skipDir   bool
}

// classifyWalkEntry maps one walked entry to either a policy exclusion, a
// capture item, or a plain continue; walkErr marks an unreadable location.
func classifyWalkEntry(sourceRoot, repository string, allowCredentials bool, path string, entry fs.DirEntry, walkErr error) (walkOutcome, error) {
	relative, relErr := filepath.Rel(sourceRoot, path)
	if walkErr != nil {
		if path == sourceRoot {
			// The source root itself is unreadable: a quiet empty capture
			// would hide a mistyped --source, so fail loudly instead.
			return walkOutcome{}, fmt.Errorf("walk source root %s: %w", sourceRoot, walkErr)
		}
		return walkOutcome{exclusion: &Exclusion{RelativePath: relative, Reason: "unreadable"}}, nil
	}
	if path == sourceRoot {
		return walkOutcome{}, nil
	}
	if relErr != nil {
		return walkOutcome{}, relErr
	}
	if isWithin(path, repository) || (isCredentialLike(relative) && !(allowCredentials && isPortableCredentialPath(relative))) || filepath.Base(relative) == ".gitconfig" {
		reason := "repository"
		if isCredentialLike(relative) {
			reason = "credential-like path"
		} else if filepath.Base(relative) == ".gitconfig" {
			reason = "handled by Git settings recipe"
		}
		return walkOutcome{exclusion: &Exclusion{RelativePath: relative, Reason: reason}, skipDir: entry.IsDir()}, nil
	}
	if reason, excluded := policyReasonFor(relative); excluded {
		return walkOutcome{exclusion: &Exclusion{RelativePath: relative, Reason: reason}, skipDir: entry.IsDir()}, nil
	}
	if entry.Type()&os.ModeSymlink != 0 {
		return walkOutcome{exclusion: &Exclusion{RelativePath: relative, Reason: "symbolic link"}, skipDir: entry.IsDir()}, nil
	}
	if entry.IsDir() {
		return walkOutcome{}, nil
	}
	info, err := entry.Info()
	if err != nil {
		return walkOutcome{exclusion: &Exclusion{RelativePath: relative, Reason: "unreadable"}}, nil
	}
	if !info.Mode().IsRegular() {
		return walkOutcome{exclusion: &Exclusion{RelativePath: relative, Reason: "unsupported file type"}}, nil
	}
	checksum, err := fileSHA256(path)
	if err != nil {
		return walkOutcome{exclusion: &Exclusion{RelativePath: relative, Reason: "unreadable"}}, nil
	}
	idSum := sha256.Sum256([]byte(relative + "\x00" + checksum))
	return walkOutcome{item: &Item{
		ID:           hex.EncodeToString(idSum[:]),
		RelativePath: filepath.ToSlash(relative),
		Size:         info.Size(),
		Mode:         uint32(info.Mode().Perm()),
		ModTimeUnix:  info.ModTime().UnixNano(),
		SHA256:       checksum,
	}}, nil
}

// reuseOrEncryptFile makes capture resumable: an interrupted capture can be
// re-run into the same repository. An existing object is only reused after it
// verifies against the manifest checksum; anything else is re-encrypted.
func reuseOrEncryptFile(source, objectPath string, key []byte, item Item) error {
	if _, statErr := os.Stat(objectPath); statErr == nil {
		if hash, size, verifyErr := verifyFile(objectPath, key, item.ID); verifyErr == nil && hash == item.SHA256 && size == item.Size {
			return nil
		}
		// The existing object is absent, unreadable, or tampered with:
		// replace it atomically with a fresh encryption.
		if err := encryptTempThenRename(source, objectPath, key, item.ID); err != nil {
			return err
		}
		return nil
	}
	return encryptObjectFunc(source, objectPath, key, item.ID)
}

// isVanishedSource reports whether err means the source file recorded during
// the walk disappeared before its turn to be sealed (the capture race with
// sync clients and browser caches that prune their own logs and caches).
func isVanishedSource(err error) bool {
	if err == nil || !os.IsNotExist(err) {
		return false
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.Errno(2) || errno == syscall.Errno(3)
	}
	return true
}

func encryptTempThenRename(source, target string, key []byte, objectID string) error {
	temporary, err := osCreateTemp(filepath.Dir(target), ".migration-object-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer removePath(temporaryPath)
	if err := fileClose(temporary); err != nil {
		return err
	}
	// encryptFile requires a fresh path (O_EXCL), so release the name first.
	if err := removePath(temporaryPath); err != nil {
		return err
	}
	if err := encryptObjectFunc(source, temporaryPath, key, objectID); err != nil {
		return err
	}
	return os.Rename(temporaryPath, target)
}

func encryptBytes(key, plaintext, aad []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	// crypto/rand.Read never returns an error on any platform (Go 1.24+).
	_, _ = rand.Read(nonce)
	return append(nonce, gcm.Seal(nil, nonce, plaintext, aad)...), nil
}

func decryptBytes(key, ciphertext, aad []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, errors.New("ciphertext is too short")
	}
	nonce, payload := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	return gcm.Open(nil, nonce, payload, aad)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, errors.New("key must be exactly 32 bytes")
	}
	// aes.NewCipher cannot fail for a validated 32-byte AES key, so its
	// error return carries no branch.
	block, _ := aes.NewCipher(key)
	return cipher.NewGCM(block)
}

func encryptFile(source, target string, key []byte, objectID string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer output.Close()
	if _, err := fileWrite(output, []byte("MKF1")); err != nil {
		return err
	}
	buffer := make([]byte, chunkSize)
	index := uint64(0)
	for {
		count, readErr := io.ReadFull(input, buffer)
		if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if count > 0 {
			// encryptBytes cannot fail for a validated 32-byte key.
			ciphertext, _ := encryptBytes(key, buffer[:count], chunkAAD(objectID, index))
			if err := writeObjectChunk(output, ciphertext); err != nil {
				return err
			}
			index++
		}
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			break
		}
	}
	return nil
}

func decryptFile(source, target string, key []byte, objectID, expectedHash string, expectedSize int64) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := osCreateTemp(filepath.Dir(target), ".migration-*.tmp")
	if err != nil {
		return err
	}
	temporary := output.Name()
	defer os.Remove(temporary)
	defer func() { _ = fileClose(output) }()
	digest, size, err := decodeObjectStream(input, output, key, objectID)
	if err != nil {
		return err
	}
	if err := fileSync(output); err != nil {
		return err
	}
	if err := fileClose(output); err != nil {
		return err
	}
	if digest != expectedHash || size != expectedSize {
		return errors.New("decrypted file does not match manifest")
	}
	return os.Rename(temporary, target)
}

// decodeObjectStream reads the MKF1 chunked object format, verifies every
// chunk against its per-chunk AAD, writes the plaintext into output, and
// returns the plaintext digest plus size.
func decodeObjectStream(input io.Reader, output io.Writer, key []byte, objectID string) (string, int64, error) {
	magic := make([]byte, 4)
	if _, err := io.ReadFull(input, magic); err != nil {
		return "", 0, err
	}
	if string(magic) != "MKF1" {
		return "", 0, errors.New("invalid encrypted file format")
	}
	hasher := sha256.New()
	var total int64
	for index := uint64(0); ; index++ {
		ciphertext, err := readChunk(input)
		if errors.Is(err, io.EOF) {
			return hex.EncodeToString(hasher.Sum(nil)), total, nil
		}
		if err != nil {
			return "", 0, err
		}
		plaintext, err := decryptBytes(key, ciphertext, chunkAAD(objectID, index))
		if err != nil {
			return "", 0, err
		}
		if _, err := output.Write(plaintext); err != nil {
			return "", 0, err
		}
		// hash.Hash.Write never returns an error per package contract.
		_, _ = hasher.Write(plaintext)
		total += int64(len(plaintext))
	}
}

func verifyFile(source string, key []byte, objectID string) (string, int64, error) {
	input, err := os.Open(source)
	if err != nil {
		return "", 0, err
	}
	defer input.Close()
	magic := make([]byte, 4)
	if _, err := io.ReadFull(input, magic); err != nil {
		return "", 0, err
	}
	if string(magic) != "MKF1" {
		return "", 0, errors.New("invalid encrypted file format")
	}
	hasher := sha256.New()
	var size int64
	for index := uint64(0); ; index++ {
		ciphertext, err := readChunk(input)
		if errors.Is(err, io.EOF) {
			return hex.EncodeToString(hasher.Sum(nil)), size, nil
		}
		if err != nil {
			return "", 0, err
		}
		plaintext, err := decryptBytes(key, ciphertext, chunkAAD(objectID, index))
		if err != nil {
			return "", 0, err
		}
		// hash.Hash.Write never returns an error per package contract.
		_, _ = hasher.Write(plaintext)
		size += int64(len(plaintext))
	}
}

// maxWireChunk is the largest payload the chunked wire format carries: the
// 32-bit length header replaces the cleartext plus the GCM overhead, and both
// writer and reader enforce the same ceiling.
const maxWireChunk = chunkSize + 12 + 16

func writeChunk(output io.Writer, payload []byte) error {
	if len(payload) > maxWireChunk {
		return errors.New("encrypted chunk is too large")
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(payload)))
	if _, err := output.Write(size[:]); err != nil {
		return err
	}
	_, err := output.Write(payload)
	return err
}

func readChunk(input io.Reader) ([]byte, error) {
	var size [4]byte
	if _, err := io.ReadFull(input, size[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(size[:])
	if length < 12+16 || length > maxWireChunk {
		return nil, errors.New("invalid encrypted chunk size")
	}
	payload := make([]byte, length)
	_, err := io.ReadFull(input, payload)
	return payload, err
}

func chunkAAD(objectID string, index uint64) []byte {
	return []byte(fmt.Sprintf("file-v1:%s:%d", objectID, index))
}

func fileSHA256(path string) (string, error) {
	input, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer input.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, input); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func isCredentialLike(path string) bool {
	clean := strings.ToLower(filepath.ToSlash(path))
	parts := strings.Split(clean, "/")
	for index, part := range parts {
		switch part {
		case ".ssh", ".gnupg", ".aws", ".azure", "keychain", "login data", "cookies":
			return true
		}
		// A two-part location: ~/.config/gcloud holds gcloud's token
		// databases, ADC files, and security caches.
		if part == ".config" && index+1 < len(parts) && parts[index+1] == "gcloud" {
			return true
		}
	}
	base := strings.ToLower(filepath.Base(clean))
	if strings.HasPrefix(base, "id_") || strings.Contains(base, "token") || strings.Contains(base, "credential") {
		return true
	}
	// auth.json-style application credential stores: provider API keys live
	// here as plain JSON. They only travel with the --credentials opt-in.
	if base == "auth.json" || strings.HasPrefix(base, "auth.json.") {
		return true
	}
	return false
}

// isPortableCredentialPath reports whether a credential-like path belongs to
// the one sanctioned opt-in class: plain-file, user-owned application
// credential stores that live under an "opencode" directory.
func isPortableCredentialPath(path string) bool {
	clean := strings.ToLower(filepath.ToSlash(path))
	parts := strings.Split(clean, "/")
	opencode := false
	for _, part := range parts {
		if strings.HasPrefix(part, "opencode") {
			opencode = true
		}
	}
	base := strings.ToLower(filepath.Base(clean))
	return opencode && (base == "auth.json" || strings.HasPrefix(base, "auth.json."))
}

// eviction policy for the default profile: paths recorded in policyPathExclusions
type policyExclusion struct {
	pattern string // slash-separated path relative to the source root, matched case-insensitively
	reason  string // short user-facing explanation recorded in the manifest
	kind    string // "prefix" matches the path and everything below it, "name" matches any base name at any depth, "name-prefix" also matches any base name with this prefix
}

// policyExclusions implements the spec's "Always Exclude" protected-path policy
// for the default profile: volatile caches, browser profiles, crash dumps,
// OS-managed application state, package caches, and credential stores are
// reproducible or not portable, so carrying them would slow the migration and
// leak private data without adding a usable file on the destination.
var policyExclusions = []policyExclusion{
	// Windows volatile and OS-managed state.
	{pattern: "AppData/Local/Temp", reason: "volatile temporary files", kind: "prefix"},
	{pattern: "AppData/Local/CrashDumps", reason: "crash dumps", kind: "prefix"},
	{pattern: "AppData/Local/D3DSCache", reason: "volatile graphics cache", kind: "prefix"},
	{pattern: "AppData/Local/Docker", reason: "container engine state and VM images", kind: "prefix"},
	{pattern: "AppData/Local/IconCache.db", reason: "volatile icon cache", kind: "name"},
	{pattern: "AppData/Local/Microsoft/Windows/INetCache", reason: "browser cache", kind: "prefix"},
	{pattern: "AppData/Local/Microsoft/Windows/WebCache", reason: "browser cache", kind: "prefix"},
	{pattern: "AppData/Local/Microsoft/WindowsApps", reason: "os-managed application stubs", kind: "prefix"},
	{pattern: "AppData/Local/NVIDIA", reason: "volatile driver cache", kind: "prefix"},
	{pattern: "AppData/Local/Packages", reason: "os-managed application container state", kind: "prefix"},
	{pattern: "AppData/Local/npm-cache", reason: "package cache", kind: "prefix"},
	{pattern: "AppData/Local/pip/cache", reason: "package cache", kind: "prefix"},
	{pattern: "AppData/Local/Yarn/Cache", reason: "package cache", kind: "prefix"},
	{pattern: "AppData/Roaming/npm-cache", reason: "package cache", kind: "prefix"},
	// Browser profiles and mail stores: excluded as a class until a
	// vendor-supported export/import adapter exists for them.
	{pattern: "AppData/Local/Google/Chrome/User Data", reason: "browser profile", kind: "prefix"},
	{pattern: "AppData/Local/Microsoft/Edge/User Data", reason: "browser profile", kind: "prefix"},
	{pattern: "AppData/Local/Mozilla/Firefox", reason: "browser profile", kind: "prefix"},
	{pattern: "AppData/Roaming/Mozilla/Firefox", reason: "browser profile", kind: "prefix"},
	{pattern: "AppData/Roaming/Thunderbird", reason: "mail client credential store", kind: "prefix"},
	// Windows registry hives and virtual memory.
	{pattern: "ntuser.dat", reason: "windows registry hive", kind: "name-prefix"},
	{pattern: "usrclass.dat", reason: "windows registry hive", kind: "name-prefix"},
	{pattern: "pagefile.sys", reason: "virtual memory", kind: "name"},
	{pattern: "hiberfil.sys", reason: "hibernation file", kind: "name"},
	{pattern: "swapfile.sys", reason: "virtual memory", kind: "name"},
	{pattern: "Thumbs.db", reason: "explorer thumbnail cache", kind: "name"},
	// macOS volatile and OS-managed state.
	{pattern: "Library/Caches", reason: "volatile cache", kind: "prefix"},
	{pattern: "Library/Cookies", reason: "browser data", kind: "prefix"},
	{pattern: "Library/Logs/CrashReporter", reason: "crash dumps", kind: "prefix"},
	{pattern: "Library/Saved Application State", reason: "volatile application state", kind: "prefix"},
	{pattern: ".Trash", reason: "trash", kind: "prefix"},
	{pattern: ".DS_Store", reason: "finder metadata", kind: "name"},
	// Linux volatile and trash locations.
	{pattern: ".cache", reason: "volatile user cache", kind: "name"},
	{pattern: ".local/share/Trash", reason: "trash", kind: "prefix"},
	{pattern: "$recycle.bin", reason: "recycle bin", kind: "name"},
	{pattern: "system volume information", reason: "os-managed system state", kind: "name"},
	// Reproducible developer caches present on every platform.
	{pattern: "go/pkg/mod", reason: "module cache", kind: "prefix"},
	{pattern: ".cargo/registry", reason: "package cache", kind: "prefix"},
	{pattern: ".gradle/caches", reason: "package cache", kind: "prefix"},
	{pattern: ".m2/repository", reason: "package cache", kind: "prefix"},
	{pattern: ".npm", reason: "package cache", kind: "prefix"},
	{pattern: ".nuget/packages", reason: "package cache", kind: "prefix"},
	{pattern: ".pnpm-store", reason: "package cache", kind: "prefix"},
	{pattern: "__pycache__", reason: "python bytecode cache", kind: "name"},
	{pattern: ".pytest_cache", reason: "python test cache", kind: "name"},
	{pattern: ".mypy_cache", reason: "python type cache", kind: "name"},
	{pattern: ".ruff_cache", reason: "python lint cache", kind: "name"},
	// Editor caches (VS Code family); configuration and snippets stay, caches go.
	{pattern: "AppData/Roaming/Code/Cache", reason: "editor cache", kind: "prefix"},
	{pattern: "AppData/Roaming/Code/CachedData", reason: "editor cache", kind: "prefix"},
	{pattern: "AppData/Roaming/Code/Code Cache", reason: "editor cache", kind: "prefix"},
	{pattern: "Library/Application Support/Code/Cache", reason: "editor cache", kind: "prefix"},
	{pattern: "Library/Application Support/Code/CachedData", reason: "editor cache", kind: "prefix"},
	{pattern: "Library/Application Support/Code/Code Cache", reason: "editor cache", kind: "prefix"},
	{pattern: ".config/Code/Cache", reason: "editor cache", kind: "prefix"},
	{pattern: ".config/Code/CachedData", reason: "editor cache", kind: "prefix"},
	{pattern: ".config/Code/Code Cache", reason: "editor cache", kind: "prefix"},
	// Tool-owned credential-adjacent files only move in their scrubbed form
	// (terminal-v1 / dev-v1 / docker-v1 / kubernetes-v1 recipes). A raw copy
	// of a shell profile or kubeconfig would carry credentials past the
	// scrubber, so the exact file paths stay out of the file inventory.
	{pattern: ".docker", reason: "docker client config (scrubbed copy moves via the docker recipe)", kind: "prefix"},
	{pattern: ".kube", reason: "kubeconfig (scrubbed copy moves via the kubernetes recipe)", kind: "prefix"},
	{pattern: ".password-store", reason: "unix pass credential store", kind: "prefix"},
	// Home-root shell profiles and tool configuration: the scrubbed recipe
	// copy is the only copy that travels. Project-local files of the same
	// name (e.g. a monorepo's .npmrc) still move with the project.
	{pattern: ".bashrc", reason: "shell profile (scrubbed copy moves via the terminal recipe)", kind: "prefix"},
	{pattern: ".profile", reason: "shell profile (scrubbed copy moves via the terminal recipe)", kind: "prefix"},
	{pattern: ".bash_aliases", reason: "shell profile (scrubbed copy moves via the terminal recipe)", kind: "prefix"},
	{pattern: ".zshrc", reason: "shell profile (scrubbed copy moves via the terminal recipe)", kind: "prefix"},
	{pattern: ".zprofile", reason: "shell profile (scrubbed copy moves via the terminal recipe)", kind: "prefix"},
	{pattern: ".tmux.conf", reason: "shell profile (scrubbed copy moves via the terminal recipe)", kind: "prefix"},
	{pattern: ".npmrc", reason: "npm config (scrubbed copy moves via the dev recipe)", kind: "prefix"},
	{pattern: ".yarnrc", reason: "yarn config (scrubbed copy moves via the dev recipe)", kind: "prefix"},
	{pattern: ".tool-versions", reason: "tool version marker (moves via the dev recipe)", kind: "prefix"},
	{pattern: ".python-version", reason: "python version marker (moves via the dev recipe)", kind: "prefix"},
	{pattern: ".node-version", reason: "node version marker (moves via the dev recipe)", kind: "prefix"},
	{pattern: ".nvmrc", reason: "nvm marker (moves via the dev recipe)", kind: "prefix"},
	{pattern: ".ruby-version", reason: "ruby version marker (moves via the dev recipe)", kind: "prefix"},
	{pattern: ".cargo/config.toml", reason: "cargo config (scrubbed copy moves via the dev recipe)", kind: "prefix"},
	{pattern: "AppData/Roaming/go/env", reason: "go environment (scrubbed copy moves via the dev recipe)", kind: "prefix"},
	{pattern: ".config/go/env", reason: "go environment (scrubbed copy moves via the dev recipe)", kind: "prefix"},
	{pattern: "AppData/Roaming/pip/pip.ini", reason: "pip config (scrubbed copy moves via the dev recipe)", kind: "prefix"},
	{pattern: ".config/pip/pip.conf", reason: "pip config (scrubbed copy moves via the dev recipe)", kind: "prefix"},
	{pattern: ".m2/settings.xml", reason: "maven settings (scrubbed copy moves via the dev recipe)", kind: "prefix"},
	{pattern: ".gradle/gradle.properties", reason: "gradle properties (scrubbed copy moves via the dev recipe)", kind: "prefix"},
	{pattern: "AppData/Local/Packages/Microsoft.WindowsTerminal_8wekyb3d8bbwe/LocalState/settings.json", reason: "terminal settings (scrubbed copy moves via the terminal recipe)", kind: "prefix"},
	{pattern: "AppData/Local/Microsoft/Windows Terminal/settings.json", reason: "terminal settings (scrubbed copy moves via the terminal recipe)", kind: "prefix"},
	{pattern: "Documents/PowerShell/Microsoft.PowerShell_profile.ps1", reason: "shell profile (scrubbed copy moves via the terminal recipe)", kind: "prefix"},
	{pattern: "Documents/PowerShell/profile.ps1", reason: "shell profile (scrubbed copy moves via the terminal recipe)", kind: "prefix"},
	{pattern: "Documents/WindowsPowerShell/Microsoft.PowerShell_profile.ps1", reason: "shell profile (scrubbed copy moves via the terminal recipe)", kind: "prefix"},
	{pattern: "Documents/WindowsPowerShell/profile.ps1", reason: "shell profile (scrubbed copy moves via the terminal recipe)", kind: "prefix"},
	{pattern: ".config/powershell/profile.ps1", reason: "shell profile (scrubbed copy moves via the terminal recipe)", kind: "prefix"},
	{pattern: ".config/powershell/Microsoft.PowerShell_profile.ps1", reason: "shell profile (scrubbed copy moves via the terminal recipe)", kind: "prefix"},
	{pattern: ".config/alacritty/alacritty.toml", reason: "terminal config (scrubbed copy moves via the terminal recipe)", kind: "prefix"},
	{pattern: ".config/kitty/kitty.conf", reason: "terminal config (scrubbed copy moves via the terminal recipe)", kind: "prefix"},
}

func policyReasonFor(relative string) (string, bool) {
	slash := strings.ToLower(filepath.ToSlash(relative))
	base := filepath.Base(slash)
	for _, exclusion := range policyExclusions {
		pattern := strings.ToLower(exclusion.pattern)
		switch exclusion.kind {
		case "prefix":
			if slash == pattern || strings.HasPrefix(slash, pattern+"/") {
				return exclusion.reason, true
			}
		case "name":
			if base == pattern {
				return exclusion.reason, true
			}
		case "name-prefix":
			if strings.HasPrefix(base, pattern) {
				return exclusion.reason, true
			}
		}
	}
	return "", false
}

func rejectSymlinkedParents(target, root string) error {
	root, err := absPath(root)
	if err != nil {
		return err
	}
	target, err = absPath(target)
	if err != nil {
		return err
	}
	current := target
	for {
		info, statErr := os.Lstat(current)
		if statErr == nil && (info.Mode()&os.ModeSymlink != 0 || hasReparsePoint(current)) {
			return errors.New("destination contains a symbolic link or reparse point")
		}
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		if current == root {
			return nil
		}
		parent := filepath.Dir(current)
		if parent == current || !isWithin(parent, root) {
			return errors.New("destination parent escapes restore root")
		}
		current = parent
	}
}

func isWithin(candidate, root string) bool {
	candidate, err := absPath(candidate)
	if err != nil {
		return false
	}
	root, err = absPath(root)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepathSeparator)))
}

const filepathSeparator = os.PathSeparator

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
	for _, plan := range PlanApplications(manifest.SourceOS, runtime.GOOS, manifest.Applications) {
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
		if err := os.MkdirAll(filepath.Dir(plan.Target), 0o700); err != nil {
			state.Items[plan.Item.ID] = ItemState{Status: "failed", Error: err.Error()}
			_ = SaveRestoreState(repository, key, state)
			return report, err
		}
		objectPath := filepath.Join(repository, objectDir, plan.Item.ID+".bin")
		if err := decryptFile(objectPath, plan.Target, key, plan.Item.ID, plan.Item.SHA256, plan.Item.Size); err != nil {
			state.Items[plan.Item.ID] = ItemState{Status: "failed", Error: err.Error()}
			_ = SaveRestoreState(repository, key, state)
			report.Add(Result{Kind: "file", ID: plan.Item.ID, Path: plan.Item.RelativePath, Status: "failed", Detail: err.Error()})
			return report, err
		}
		state.Items[plan.Item.ID] = ItemState{Status: "copied"}
		if err := SaveRestoreState(repository, key, state); err != nil {
			return report, err
		}
		report.Add(Result{Kind: "file", ID: plan.Item.ID, Path: plan.Item.RelativePath, Status: "copied"})
	}
	if installApplications && len(manifest.Applications) > 0 {
		plans := PlanApplications(manifest.SourceOS, runtime.GOOS, manifest.Applications)
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
			tempDir, tempErr := os.MkdirTemp("", "migration-kit-apps-")
			if tempErr != nil {
				return report, tempErr
			}
			installErr := error(nil)
			if runtime.GOOS == "windows" && allManager(installable, "winget") {
				installErr = installWindowsApplications(context.Background(), systemCommandRunner{}, installable, filepath.Join(tempDir, "winget-import.json"))
			} else {
				for _, application := range installable {
					if err := InstallApplication(context.Background(), systemCommandRunner{}, application); err != nil {
						installErr = err
					}
				}
			}
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
		}
	}
	for _, setting := range manifest.Settings {
		if setting.Recipe != "git-v1" {
			report.Add(Result{Kind: "setting", ID: setting.Recipe, Status: "unsupported"})
			continue
		}
		content, renderErr := RenderGitSetting(setting)
		if renderErr != nil {
			return report, renderErr
		}
		path := filepath.Join(destination, ".gitconfig")
		if _, statErr := os.Stat(path); statErr == nil && !overwrite {
			report.Add(Result{Kind: "setting", ID: setting.Recipe, Path: path, Status: "skipped-existing"})
			continue
		}
		if writeErr := os.WriteFile(path, content, 0o600); writeErr != nil {
			return report, writeErr
		}
		report.Add(Result{Kind: "setting", ID: setting.Recipe, Path: path, Status: "restored"})
	}
	report.Finish()
	return report, nil
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
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	return key, nil
}

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
	return CaptureWithOptions(sourceRoot, repository, key, false, systemCommandRunner{})
}

func CaptureWithOptions(sourceRoot, repository string, key []byte, includeApplications bool, runner CommandRunner) (Manifest, error) {
	if len(key) != KeySize {
		return Manifest{}, errors.New("key must be exactly 32 bytes")
	}
	sourceRoot, err := filepath.Abs(sourceRoot)
	if err != nil {
		return Manifest{}, fmt.Errorf("resolve source: %w", err)
	}
	repository, err = filepath.Abs(repository)
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
	items, exclusions, err := inventory(sourceRoot, repository)
	if err != nil {
		return Manifest{}, err
	}
	var applications []Application
	if includeApplications {
		if runner == nil {
			runner = systemCommandRunner{}
		}
		tempDir, err := os.MkdirTemp("", "migration-kit-apps-")
		if err != nil {
			return Manifest{}, fmt.Errorf("create application inventory workspace: %w", err)
		}
		defer os.RemoveAll(tempDir)
		applications, err = inventoryApplications(context.Background(), runner, tempDir)
		if err != nil {
			exclusions = append(exclusions, Exclusion{RelativePath: "applications", Reason: "application inventory unavailable: " + err.Error()})
			applications = nil
		}
	}
	settings := []Setting{}
	if setting, settingErr := CaptureGitSetting(sourceRoot); settingErr == nil && len(setting.Values) > 0 {
		settings = append(settings, setting)
	}
	for index := range items {
		path := filepath.Join(sourceRoot, filepath.FromSlash(items[index].RelativePath))
		objectPath := filepath.Join(repository, objectDir, items[index].ID+".bin")
		if err := encryptFile(path, objectPath, key, items[index].ID); err != nil {
			return Manifest{}, fmt.Errorf("encrypt %s: %w", items[index].RelativePath, err)
		}
	}
	manifest := Manifest{
		Version:      1,
		CreatedAt:    time.Now().UTC(),
		SourceOS:     runtime.GOOS,
		SourceRoot:   filepath.Base(sourceRoot),
		Items:        items,
		Exclusions:   exclusions,
		Applications: applications,
		Settings:     settings,
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return Manifest{}, fmt.Errorf("encode manifest: %w", err)
	}
	ciphertext, err := encryptBytes(key, encoded, []byte("manifest-v1"))
	if err != nil {
		return Manifest{}, fmt.Errorf("encrypt manifest: %w", err)
	}
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
	destination, err := filepath.Abs(destination)
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
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect destination %s: %w", item.RelativePath, err)
		}
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
		if err := os.Chmod(plan.Target, fs.FileMode(plan.Item.Mode)); err != nil {
			return fmt.Errorf("restore permissions %s: %w", plan.Item.RelativePath, err)
		}
	}
	if installApplications && len(manifest.Applications) > 0 {
		if runner == nil {
			runner = systemCommandRunner{}
		}
		tempDir, err := os.MkdirTemp("", "migration-kit-apps-")
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
	var items []Item
	var exclusions []Exclusion
	err := filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			relative, _ := filepath.Rel(sourceRoot, path)
			exclusions = append(exclusions, Exclusion{RelativePath: relative, Reason: "unreadable"})
			return nil
		}
		if path == sourceRoot {
			return nil
		}
		relative, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		if isWithin(path, repository) || isCredentialLike(relative) || filepath.Base(relative) == ".gitconfig" {
			reason := "repository"
			if isCredentialLike(relative) {
				reason = "credential-like path"
			} else if filepath.Base(relative) == ".gitconfig" {
				reason = "handled by Git settings recipe"
			}
			exclusions = append(exclusions, Exclusion{RelativePath: relative, Reason: reason})
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			exclusions = append(exclusions, Exclusion{RelativePath: relative, Reason: "symbolic link"})
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			exclusions = append(exclusions, Exclusion{RelativePath: relative, Reason: "unreadable"})
			return nil
		}
		if !info.Mode().IsRegular() {
			exclusions = append(exclusions, Exclusion{RelativePath: relative, Reason: "unsupported file type"})
			return nil
		}
		checksum, err := fileSHA256(path)
		if err != nil {
			exclusions = append(exclusions, Exclusion{RelativePath: relative, Reason: "unreadable"})
			return nil
		}
		idInput := relative + "\x00" + checksum
		idSum := sha256.Sum256([]byte(idInput))
		items = append(items, Item{
			ID:           hex.EncodeToString(idSum[:]),
			RelativePath: filepath.ToSlash(relative),
			Size:         info.Size(),
			Mode:         uint32(info.Mode().Perm()),
			ModTimeUnix:  info.ModTime().UnixNano(),
			SHA256:       checksum,
		})
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("walk source: %w", err)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].RelativePath < items[j].RelativePath })
	sort.Slice(exclusions, func(i, j int) bool { return exclusions[i].RelativePath < exclusions[j].RelativePath })
	return items, exclusions, nil
}

func encryptBytes(key, plaintext, aad []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
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
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
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
	if _, err := output.Write([]byte("MKF1")); err != nil {
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
			ciphertext, err := encryptBytes(key, buffer[:count], chunkAAD(objectID, index))
			if err != nil {
				return err
			}
			if err := writeChunk(output, ciphertext); err != nil {
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
	output, err := os.CreateTemp(filepath.Dir(target), ".migration-*.tmp")
	if err != nil {
		return err
	}
	temporary := output.Name()
	defer os.Remove(temporary)
	defer output.Close()
	magic := make([]byte, 4)
	if _, err := io.ReadFull(input, magic); err != nil {
		return err
	}
	if string(magic) != "MKF1" {
		return errors.New("invalid encrypted file format")
	}
	for index := uint64(0); ; index++ {
		ciphertext, err := readChunk(input)
		if errors.Is(err, io.EOF) {
			if err := output.Sync(); err != nil {
				return err
			}
			if err := output.Close(); err != nil {
				return err
			}
			actualHash, err := fileSHA256(temporary)
			if err != nil {
				return err
			}
			info, err := os.Stat(temporary)
			if err != nil {
				return err
			}
			if actualHash != expectedHash || info.Size() != expectedSize {
				return errors.New("decrypted file does not match manifest")
			}
			return os.Rename(temporary, target)
		}
		if err != nil {
			return err
		}
		plaintext, err := decryptBytes(key, ciphertext, chunkAAD(objectID, index))
		if err != nil {
			return err
		}
		if _, err := output.Write(plaintext); err != nil {
			return err
		}
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
		written, err := hasher.Write(plaintext)
		if err != nil {
			return "", 0, err
		}
		size += int64(written)
	}
}

func writeChunk(output io.Writer, payload []byte) error {
	if len(payload) > int(^uint32(0)) {
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
	if length < 12+16 || length > chunkSize+12+16 {
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
	for _, part := range parts {
		switch part {
		case ".ssh", ".gnupg", ".aws", ".azure", ".config/gcloud", "keychain", "login data", "cookies":
			return true
		}
	}
	base := strings.ToLower(filepath.Base(clean))
	return strings.HasPrefix(base, "id_") || strings.Contains(base, "token") || strings.Contains(base, "credential")
}

func rejectSymlinkedParents(target, root string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return err
	}
	current := target
	for {
		info, statErr := os.Lstat(current)
		if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return errors.New("destination contains a symbolic link")
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
	candidate, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	root, err = filepath.Abs(root)
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

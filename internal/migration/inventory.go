package migration

// Inventory: deterministic capture-walk decisions shared by capture and the
// dev-recipe catalog. Every excluded entry carries a reason so the destination
// report and the checklist stay faithful to what traveled and what did not.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// policyDirectories are destination-owned layout names that never hold
// user source material worth carrying (temp dirs of the toolchain family).
var policyDirectories = []string{"AppData/Local/Temp"}

// catalogPathIndex maps slash-relatives in devFileCatalog (all platform
// spellings) to the spec key; catalogDirs indexes their parent directories so
// whole spec-owned directories (e.g. .docker) are skipped once.
var (
	catalogPathIndex = map[string]string{}
	catalogDirs      = map[string]string{}
)

func init() {
	for _, spec := range devFileCatalog {
		for _, relative := range spec.Paths {
			slashes := filepath.ToSlash(relative)
			catalogPathIndex[slashes] = spec.Key
			if dir := filepath.ToSlash(filepath.Dir(slashes)); dir != "." && dir != "/" {
				catalogDirs[dir] = spec.Key
			}
		}
	}
}

type walkOutcome struct {
	item      *Item
	exclusion *Exclusion
	skipDir   bool
}

// classifyWalkEntry decides one walk entry for the inventory: item, exclusion,
// recurse, or nothing. A path that cannot be relativised to the source root is
// a hard walk failure; everything else is recorded, never fatal.
func classifyWalkEntry(sourceRoot, repository string, includeApplications bool, path string, entry fs.DirEntry, walkErr error) (walkOutcome, error) {
	if walkErr != nil {
		relative, _ := filepath.Rel(sourceRoot, path)
		return walkOutcome{exclusion: &Exclusion{RelativePath: filepath.ToSlash(relative), Reason: "unreadable"}}, nil
	}
	if path == sourceRoot {
		return walkOutcome{}, nil
	}
	relative, err := filepath.Rel(sourceRoot, path)
	if err != nil {
		return walkOutcome{}, err
	}
	slashed := filepath.ToSlash(relative)

	for _, policy := range policyDirectories {
		if slashed == policy || strings.HasPrefix(slashed, policy+"/") {
			return walkOutcome{exclusion: &Exclusion{RelativePath: policy, Reason: "destination-owned directory"}, skipDir: true}, nil
		}
	}
	if isWithin(path, repository) {
		return walkOutcome{exclusion: &Exclusion{RelativePath: slashed, Reason: "repository"}, skipDir: true}, nil
	}
	if key, ok := catalogDirs[slashed]; ok && entry.IsDir() {
		return walkOutcome{exclusion: &Exclusion{RelativePath: slashed, Reason: "development config captured as a recipe (" + key + ")"}, skipDir: true}, nil
	}
	if _, ok := catalogPathIndex[slashed]; ok && !entry.IsDir() {
		return walkOutcome{exclusion: &Exclusion{RelativePath: slashed, Reason: "development config captured as a recipe"}}, nil
	}
	if filepath.Base(slashed) == ".gitconfig" {
		return walkOutcome{exclusion: &Exclusion{RelativePath: slashed, Reason: "handled by Git settings recipe"}}, nil
	}
	// The opencode auth.json is the single deliberate opt-in: it is the only file
	// whose secrets travel when the operator explicitly asked (--credentials).
	if isCredentialLike(slashed) && !(includeApplications && isPortableCredentialPath(slashed)) {
		return walkOutcome{exclusion: &Exclusion{RelativePath: slashed, Reason: "credential-like path"}, skipDir: true}, nil
	}
	if isPortableCredentialPath(slashed) && !includeApplications {
		return walkOutcome{exclusion: &Exclusion{RelativePath: slashed, Reason: "credential-like path"}}, nil
	}
	if entry.Type()&os.ModeSymlink != 0 {
		return walkOutcome{exclusion: &Exclusion{RelativePath: slashed, Reason: "symbolic link"}}, nil
	}
	if entry.IsDir() {
		return walkOutcome{}, nil
	}
	info, err := entry.Info()
	if err != nil {
		return walkOutcome{exclusion: &Exclusion{RelativePath: slashed, Reason: "unreadable"}}, nil
	}
	if !info.Mode().IsRegular() {
		return walkOutcome{exclusion: &Exclusion{RelativePath: slashed, Reason: "unsupported file type"}}, nil
	}
	checksum, err := fileSHA256(path)
	if err != nil {
		return walkOutcome{exclusion: &Exclusion{RelativePath: slashed, Reason: "unreadable"}}, nil
	}
	idSum := sha256.Sum256([]byte(slashed + "\x00" + checksum))
	return walkOutcome{item: &Item{
		ID:           hex.EncodeToString(idSum[:]),
		RelativePath: slashed,
		Size:         info.Size(),
		Mode:         uint32(info.Mode().Perm()),
		ModTimeUnix:  info.ModTime().UnixNano(),
		SHA256:       checksum,
	}}, nil
}

func inventoryWithOptions(sourceRoot, repository string, includeApplications bool) ([]Item, []Exclusion, error) {
	sourceRoot, err := filepath.Abs(sourceRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve source: %w", err)
	}
	repository, err = filepath.Abs(repository)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve repository: %w", err)
	}
	if _, err := os.Stat(sourceRoot); err != nil {
		return nil, nil, err
	}
	items := []Item{}
	exclusions := []Exclusion{}
	err = filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		outcome, err := classifyWalkEntry(sourceRoot, repository, includeApplications, path, entry, walkErr)
		if err != nil {
			return err
		}
		if outcome.item != nil {
			items = append(items, *outcome.item)
		}
		if outcome.exclusion != nil {
			exclusions = append(exclusions, *outcome.exclusion)
		}
		if outcome.skipDir && entry.IsDir() {
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

// isPortableCredentialPath marks opencode auth.json wherever it lives: the
// profile's agent secrets, leading "auth.json" under any opencode directory.
func isPortableCredentialPath(path string) bool {
	clean := filepath.ToSlash(path)
	parts := strings.Split(strings.ToLower(clean), "/")
	if len(parts) < 2 {
		return false
	}
	base := parts[len(parts)-1]
	if !strings.HasPrefix(base, "auth.json") {
		return false
	}
	for _, dir := range parts[:len(parts)-1] {
		if dir == "opencode" {
			return true
		}
	}
	return false
}

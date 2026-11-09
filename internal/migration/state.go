package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const restoreStateFile = "restore-state.enc"

type ItemState struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type RestoreState struct {
	Version           int                  `json:"version"`
	BundleFingerprint string               `json:"bundle_fingerprint"`
	Destination       string               `json:"destination"`
	Profile           string               `json:"profile"`
	PlanFingerprint   string               `json:"plan_fingerprint"`
	Overwrite         bool                 `json:"overwrite"`
	DryRunCompleted   bool                 `json:"dry_run_completed"`
	UpdatedAt         time.Time            `json:"updated_at"`
	Items             map[string]ItemState `json:"items"`
}

func NewRestoreState(manifest Manifest, destination, profile string, overwrite bool) (RestoreState, error) {
	bundle := BundleFingerprint(manifest)
	plan, err := PlanFingerprint(manifest, destination, profile, overwrite)
	if err != nil {
		return RestoreState{}, err
	}
	return RestoreState{
		Version:           1,
		BundleFingerprint: bundle,
		Destination:       destination,
		Profile:           profile,
		PlanFingerprint:   plan,
		Overwrite:         overwrite,
		Items:             map[string]ItemState{},
	}, nil
}

func BundleFingerprint(manifest Manifest) string {
	encoded, _ := json.Marshal(manifest)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func PlanFingerprint(manifest Manifest, destination, profile string, overwrite bool) (string, error) {
	bundle := BundleFingerprint(manifest)
	if _, err := absPath(destination); err != nil {
		return "", err
	}
	fingerprint := struct {
		Bundle      string `json:"bundle"`
		Destination string `json:"destination"`
		Profile     string `json:"profile"`
		Overwrite   bool   `json:"overwrite"`
	}{
		Bundle:      bundle,
		Destination: destination,
		Profile:     profile,
		Overwrite:   overwrite,
	}
	encoded, err := json.Marshal(fingerprint)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// SaveRestoreState persists the ledger atomically: stage in a temp file,
// close, publish by rename. Every stage runs through the file seams so
// fault-injection tests model a dying disk.
func SaveRestoreState(repository string, key []byte, state RestoreState) error {
	if state.Version == 0 {
		state.Version = 1
	}
	if state.Items == nil {
		state.Items = map[string]ItemState{}
	}
	state.UpdatedAt = time.Now().UTC()
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	encrypted, err := encryptBytes(key, encoded, []byte("restore-state-v1"))
	if err != nil {
		return err
	}
	temp, err := osCreateTemp(repository, ".restore-state-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temp.Name()
	discard := func() { _ = removePath(temporaryPath) }
	if err := fileChmod(temp, 0o600); err != nil {
		_ = fileClose(temp)
		discard()
		return err
	}
	if _, err := fileWrite(temp, encrypted); err != nil {
		_ = fileClose(temp)
		discard()
		return err
	}
	if err := fileSync(temp); err != nil {
		_ = fileClose(temp)
		discard()
		return err
	}
	if err := fileClose(temp); err != nil {
		discard()
		return err
	}
	if err := os.Rename(temporaryPath, filepath.Join(repository, restoreStateFile)); err != nil {
		discard()
		return err
	}
	discard()
	return nil
}

func LoadRestoreState(repository string, key []byte) (RestoreState, error) {
	contents, err := os.ReadFile(filepath.Join(repository, restoreStateFile))
	if err != nil {
		return RestoreState{}, err
	}
	plaintext, err := decryptBytes(key, contents, []byte("restore-state-v1"))
	if err != nil {
		return RestoreState{}, fmt.Errorf("decrypt restore state: %w", err)
	}
	var state RestoreState
	if err := json.Unmarshal(plaintext, &state); err != nil {
		return RestoreState{}, fmt.Errorf("decode restore state: %w", err)
	}
	if state.Version != 1 {
		return RestoreState{}, fmt.Errorf("unsupported restore state version %d", state.Version)
	}
	if state.Items == nil {
		state.Items = map[string]ItemState{}
	}
	return state, nil
}

func requireMatchingDryRun(state RestoreState, planFingerprint string) error {
	if !state.DryRunCompleted {
		return errors.New("restore requires a completed dry-run")
	}
	if state.PlanFingerprint != planFingerprint {
		return errors.New("restore plan no longer matches the completed dry-run")
	}
	return nil
}

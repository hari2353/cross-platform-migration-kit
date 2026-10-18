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
	// json.Marshal cannot fail for this primitives-only shape.
	encoded, _ := json.Marshal(manifest)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func PlanFingerprint(manifest Manifest, destination, profile string, overwrite bool) (string, error) {
	fingerprint := struct {
		Bundle      string `json:"bundle"`
		Destination string `json:"destination"`
		Profile     string `json:"profile"`
		Overwrite   bool   `json:"overwrite"`
	}{}
	bundle := BundleFingerprint(manifest)
	destination, err := absPath(destination)
	if err != nil {
		return "", err
	}
	fingerprint.Bundle = bundle
	fingerprint.Destination = destination
	fingerprint.Profile = profile
	fingerprint.Overwrite = overwrite
	// json.Marshal cannot fail for this primitives-only shape.
	encoded, _ := json.Marshal(fingerprint)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func SaveRestoreState(repository string, key []byte, state RestoreState) error {
	if state.Version == 0 {
		state.Version = 1
	}
	if state.Items == nil {
		state.Items = map[string]ItemState{}
	}
	state.UpdatedAt = time.Now().UTC()
	// json.Marshal cannot fail for this primitives-only shape.
	encoded, _ := json.Marshal(state)
	// encryptBytes cannot fail for a validated 32-byte key.
	encrypted, _ := encryptBytes(key, encoded, []byte("restore-state-v1"))
	temporary, err := osCreateTemp(repository, ".restore-state-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := fileChmod(temporary, 0o600); err != nil {
		fileClose(temporary)
		return err
	}
	if _, err := fileWrite(temporary, encrypted); err != nil {
		fileClose(temporary)
		return err
	}
	if err := fileSync(temporary); err != nil {
		fileClose(temporary)
		return err
	}
	if err := fileClose(temporary); err != nil {
		return err
	}
	return os.Rename(temporaryPath, filepath.Join(repository, restoreStateFile))
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

package migration

import (
	"fmt"
	"os"
	"path/filepath"
)

func Doctor(repository, keyPath, destination string) []Result {
	results := []Result{}
	if repository == "" {
		return append(results, Result{Kind: "bundle", Status: "invalid", Detail: "bundle path is required"})
	}
	if _, err := os.Stat(filepath.Join(repository, formatFile)); err != nil {
		results = append(results, Result{Kind: "bundle", Status: "invalid", Detail: "repository format is missing"})
	} else {
		results = append(results, Result{Kind: "bundle", Status: "available"})
	}
	var key []byte
	if keyPath == "" {
		results = append(results, Result{Kind: "key", Status: "invalid", Detail: "key path is required"})
	} else if loaded, err := ReadKey(keyPath); err != nil {
		results = append(results, Result{Kind: "key", Status: "missing"})
	} else {
		key = loaded
		results = append(results, Result{Kind: "key", Status: "available"})
	}
	if len(key) == KeySize && repository != "" {
		if manifest, err := LoadManifest(repository, key); err != nil {
			results = append(results, Result{Kind: "bundle", Status: "unreadable", Detail: "manifest authentication failed"})
		} else if err := Verify(repository, key); err != nil {
			results = append(results, Result{Kind: "bundle", Status: "corrupt", Detail: "encrypted object verification failed"})
		} else {
			results = append(results, Result{Kind: "manifest", Status: "verified", Detail: fmt.Sprintf("%d files, %d applications", len(manifest.Items), len(manifest.Applications))})
		}
	}
	if destination != "" {
		if info, err := os.Stat(destination); err != nil {
			results = append(results, Result{Kind: "destination", Status: "missing", Detail: err.Error()})
		} else if !info.IsDir() {
			results = append(results, Result{Kind: "destination", Status: "invalid", Detail: "destination is not a directory"})
		} else {
			results = append(results, Result{Kind: "destination", Status: "available"})
		}
	}
	return results
}

package migration

import (
	"path/filepath"
	"runtime"
	"testing"
)

// Steal: Zinstall Migration Kit Pro's $189 headline is "transfer from a
// broken computer" — a mounted drive that is not a running OS. This kit's
// --source root already accepts any mounted volume; what it lacked was
// classifying that volume's OS from its CONTENT. runtime.GOOS reported the
// HOST's OS, so a Windows drive captured on a Mac claimed to be macOS —
// and every downstream cross-platform mapping lied.

func captureSourceOS(t *testing.T, marker string) string {
	t.Helper()
	source := t.TempDir()
	repository := filepath.Join(t.TempDir(), "repository")
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(source, filepath.FromSlash(marker)), "marker")
	manifest, err := Capture(source, repository, key)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	return manifest.SourceOS
}

func TestCaptureClassifiesSourceOSFromTreeShape(t *testing.T) {
	cases := []struct {
		marker, hostOS, want string
	}{
		{"AppData/Local/Settings/marker.txt", "darwin", "windows"},
		{"AppData/Roaming/marker.txt", "darwin", "windows"},
		{"Library/Application Support/marker.txt", "windows", "darwin"},
		{".config/git/config", "darwin", "linux"},
		{".local/share/marker.txt", "darwin", "linux"},
	}
	for _, tc := range cases {
		if got := captureSourceOS(t, tc.marker); got != tc.want {
			t.Errorf("tree with %s (host %s) classified as %q, want %q",
				tc.marker, tc.hostOS, got, tc.want)
		}
	}
}

func TestCaptureFallsBackToHostOSForAmbiguousTrees(t *testing.T) {
	// a bare Documents tree carries no platform marker — the host OS is the
	// only remaining signal, and that is honest for same-platform upgrades
	if got := captureSourceOS(t, "Documents/one.txt"); got != runtime.GOOS {
		t.Errorf("ambiguous tree classified as %q, want host %q", got, runtime.GOOS)
	}
}

package migration

// ConflictResolution is the outcome of resolving a sync conflict.
type ConflictResolution struct {
	Winner string // "local", "remote", or "manual"
	Reason string
}

// ResolveConflict picks a winner between local and remote versions using
// last-write-wins with a vector-clock tie-break. Cross-pollinated from
// Dropbox's sync protocol (Inside the Magic Packet).
func ResolveConflict(localVersion, remoteVersion int, localTime, remoteTime int64) ConflictResolution {
	if localTime > remoteTime {
		return ConflictResolution{Winner: "local", Reason: "local is newer"}
	}
	if remoteTime > localTime {
		return ConflictResolution{Winner: "remote", Reason: "remote is newer"}
	}
	// Tie on timestamp: higher version wins.
	if localVersion > remoteVersion {
		return ConflictResolution{Winner: "local", Reason: "tie broken by version"}
	}
	if remoteVersion > localVersion {
		return ConflictResolution{Winner: "remote", Reason: "tie broken by version"}
	}
	return ConflictResolution{Winner: "manual", Reason: "exact tie, needs manual resolution"}
}

// MergeThreeWay decides whether a three-way merge is clean.
func MergeThreeWay(base, local, remote string) (string, bool) {
	if local == remote {
		return local, true
	}
	if local == base {
		return remote, true
	}
	if remote == base {
		return local, true
	}
	return "", false
}

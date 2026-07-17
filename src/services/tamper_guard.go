package services

// protectedFile is one guarded file's pre-invocation state. An absent file is
// a known state (guarded, not existing); a file that exists but cannot be read
// is left unguarded for that iteration, since restoring it could destroy data.
type protectedFile struct {
	path    string
	guarded bool
	exists  bool
	content string
}

// TamperGuard detects and reverts tool modifications to the protocol files
// that define the work's success criteria (the plan, tests, and BDD criteria).
// STEPS.md is deliberately not guarded: checking boxes there is the tool's
// job. The guard snapshots each protected file before an invocation and
// restores any file whose content changed, so a tool cannot weaken its own
// definition of done to pass verification.
type TamperGuard struct {
	files FileStore
	paths []string
}

// NewTamperGuard wires a guard over the given protected file paths.
func NewTamperGuard(files FileStore, paths []string) *TamperGuard {
	return &TamperGuard{files: files, paths: paths}
}

// Snapshot captures each protected file's current content or absence.
func (g *TamperGuard) Snapshot() []protectedFile {
	snapshot := make([]protectedFile, 0, len(g.paths))
	for _, path := range g.paths {
		snapshot = append(snapshot, g.capture(path))
	}
	return snapshot
}

func (g *TamperGuard) capture(path string) protectedFile {
	if !g.files.Exists(path) {
		return protectedFile{path: path, guarded: true}
	}
	content, err := g.files.Read(path)
	if err != nil {
		return protectedFile{path: path}
	}
	return protectedFile{path: path, guarded: true, exists: true, content: content}
}

// RestoreTampered reverts every protected file whose state changed since the
// snapshot and returns the paths it successfully restored.
func (g *TamperGuard) RestoreTampered(snapshot []protectedFile) []string {
	var restored []string
	for _, before := range snapshot {
		if g.restore(before) {
			restored = append(restored, before.path)
		}
	}
	return restored
}

// restore reverts one file to its snapshot state, reporting whether a change
// was found and successfully undone. A file that is unreadable after the run
// counts as changed: writing the snapshot back is the safe recovery.
func (g *TamperGuard) restore(before protectedFile) bool {
	if !before.guarded {
		return false
	}
	after := g.capture(before.path)
	if after.guarded && after.exists == before.exists && after.content == before.content {
		return false
	}
	if !before.exists {
		return g.files.Remove(before.path) == nil
	}
	return g.files.Write(before.path, before.content) == nil
}

package updater

import (
	"os"
	"path/filepath"
)

type locationProblem struct {
	message string
	cause   error
}

func (e *locationProblem) Error() string { return e.message + ": " + e.cause.Error() }
func (e *locationProblem) Unwrap() error { return e.cause }

func checkExecutableLocation(executable string) error {
	target, err := installationTarget(executable)
	if err != nil {
		return err
	}
	return checkInstallLocation(target)
}

// Probe the actual installation directory, regardless of its name. In
// particular, macOS installations do not have to be inside /Applications.
// Only files created by this probe are renamed or removed.
func checkInstallLocation(target string) error {
	f, err := os.CreateTemp(filepath.Dir(target), ".bbdown-write-check-*")
	if err != nil {
		return locationError(target, err)
	}
	path := f.Name()
	defer os.Remove(path)
	if err := f.Close(); err != nil {
		return locationError(target, err)
	}
	renamed := path + ".renamed"
	defer os.Remove(renamed)
	if err := os.Rename(path, renamed); err != nil {
		return locationError(target, err)
	}
	if err := os.Remove(renamed); err != nil {
		return locationError(target, err)
	}
	return nil
}

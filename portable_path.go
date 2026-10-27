package main

import (
	"errors"
	"os"
	"path/filepath"
)

// Set only by the portable packaging script. go run must never use its temporary EXE.
var portableBuild = "false"

var executableLocation = os.Executable

func portableOutputCandidate() (string, error) {
	if portableBuild != "true" {
		return "", errors.New("portable output directory is unavailable in source builds")
	}
	executable, err := executableLocation()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(executable) {
		return "", errors.New("executable location is not absolute")
	}
	return filepath.Join(filepath.Dir(executable), "exports"), nil
}

func resolveExplicitOutputDirectory(directory string) (string, error) {
	directory = expandHome(directory)
	if portableBuild != "true" || filepath.IsAbs(directory) {
		return filepath.Abs(directory)
	}
	if !filepath.IsLocal(directory) {
		return "", errors.New("relative portable directory must stay within the executable directory; use an absolute path for another location")
	}
	candidate, err := portableOutputCandidate()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(candidate), directory), nil
}

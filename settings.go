package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var errNoOutputDirectory = errors.New("no output directory configured; use -set-output-dir <directory> to save a default, -output-dir <directory> for this run, or -output <file> for a single export")

type userSettings struct {
	OutputDirectory string `json:"output_directory"`
}

func settingsPath() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find settings directory: %w", err)
	}
	return filepath.Join(root, "codex-exporter", "settings.json"), nil
}

func savedOutputRoot() (string, error) {
	path, err := settingsPath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", errNoOutputDirectory
	}
	if err != nil {
		return "", fmt.Errorf("read output directory settings: %w", err)
	}
	var settings userSettings
	if err := json.Unmarshal(data, &settings); err != nil || !filepath.IsAbs(settings.OutputDirectory) {
		return "", errors.New("invalid output directory settings; repair with -set-output-dir <directory>")
	}
	return filepath.Clean(settings.OutputDirectory), nil
}

func saveOutputRoot(directory string) error {
	if strings.TrimSpace(directory) == "" {
		return errors.New("default output directory must not be empty")
	}
	directory, err := filepath.Abs(expandHome(directory))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create chosen output directory: %w", err)
	}
	path, err := settingsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	encodeErr := json.NewEncoder(temp).Encode(userSettings{OutputDirectory: directory})
	syncErr := temp.Sync()
	closeErr := temp.Close()
	if err := errors.Join(encodeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("write output directory settings: %w", err)
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return fmt.Errorf("save output directory settings: %w", err)
	}
	return nil
}

func consoleInteractive() bool {
	input, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	output, err := os.Stdout.Stat()
	return err == nil && input.Mode()&os.ModeCharDevice != 0 && output.Mode()&os.ModeCharDevice != 0
}

func chooseOutputRoot(input io.Reader, output io.Writer, interactive bool) (string, error) {
	root, err := savedOutputRoot()
	if err == nil {
		return root, nil
	}
	if !errors.Is(err, errNoOutputDirectory) || !interactive {
		return "", err
	}
	reader := bufio.NewReader(input)
	fmt.Fprint(output, "No default output directory. Enter an export directory: ")
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("%w; interactive input unavailable: %v", errNoOutputDirectory, err)
	}
	directory := strings.TrimSpace(line)
	if directory == "" {
		return "", errNoOutputDirectory
	}
	directory, err = filepath.Abs(expandHome(directory))
	if err != nil {
		return "", err
	}
	fmt.Fprint(output, "Save this directory as your default? [y/N]: ")
	answer, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("%w; save confirmation unavailable: %v", errNoOutputDirectory, err)
	}
	if strings.EqualFold(strings.TrimSpace(answer), "y") || strings.EqualFold(strings.TrimSpace(answer), "yes") {
		if err := saveOutputRoot(directory); err != nil {
			return "", err
		}
	}
	return directory, nil
}

// Package paths resolves OS-appropriate directories across Linux (XDG),
// macOS (Application Support), and Windows (%APPDATA%) without hardcoding
// user-specific or device-specific paths.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

const AppDirName = "Lagrangian"

// Layout describes the resolved filesystem locations for a Lagrangian instance.
type Layout struct {
	ConfigDir      string
	ConfigFile     string
	DataDir        string
	DBPath         string
	AttachmentsDir string
	SecretsDir     string
}

// Resolve computes default paths following OS conventions.
// When explicitHome or LAGRANGIAN_HOME is set, all directories are placed under it.
func Resolve(explicitHome string) (Layout, error) {
	home := explicitHome
	if home == "" {
		home = os.Getenv("LAGRANGIAN_HOME")
	}
	if home != "" {
		abs, err := filepath.Abs(home)
		if err != nil {
			return Layout{}, err
		}
		return Layout{
			ConfigDir:      abs,
			ConfigFile:     filepath.Join(abs, "config.yaml"),
			DataDir:        filepath.Join(abs, "data"),
			DBPath:         filepath.Join(abs, "data", "lagrangian.sqlite"),
			AttachmentsDir: filepath.Join(abs, "data", "attachments"),
			SecretsDir:     filepath.Join(abs, "secrets"),
		}, nil
	}

	cfgBase, err := os.UserConfigDir()
	if err != nil {
		return Layout{}, err
	}
	dataBase, err := userDataDir(cfgBase)
	if err != nil {
		return Layout{}, err
	}

	dirName := AppDirName
	if runtime.GOOS == "linux" {
		dirName = "lagrangian"
	}

	cfgDir := filepath.Join(cfgBase, dirName)
	dataDir := filepath.Join(dataBase, dirName)

	return Layout{
		ConfigDir:      cfgDir,
		ConfigFile:     filepath.Join(cfgDir, "config.yaml"),
		DataDir:        dataDir,
		DBPath:         filepath.Join(dataDir, "lagrangian.sqlite"),
		AttachmentsDir: filepath.Join(dataDir, "attachments"),
		SecretsDir:     filepath.Join(dataDir, "secrets"),
	}, nil
}

func userDataDir(configBase string) (string, error) {
	switch runtime.GOOS {
	case "darwin", "windows":
		return configBase, nil
	default:
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			return xdg, nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "share"), nil
	}
}

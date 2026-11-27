//go:build darwin

package secrets

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const keychainService = "Lagrangian"

// KeychainStore accesses macOS Keychain via the system security CLI without CGO.
type KeychainStore struct {
	service string
}

// Open selects the platform secret store. Setting LAGRANGIAN_SECRET_BACKEND=file
// forces the AES-256-GCM encrypted file vault (useful in headless SSH/launchd daemons).
func Open(fallbackDir string) (Store, error) {
	if os.Getenv("LAGRANGIAN_SECRET_BACKEND") == "file" {
		return NewFileStore(fallbackDir)
	}
	if _, err := exec.LookPath("security"); err != nil {
		return NewFileStore(fallbackDir)
	}
	return &KeychainStore{service: keychainService}, nil
}

func (k *KeychainStore) BackendName() string {
	return "macos_keychain"
}

func (k *KeychainStore) Get(ref string) (string, error) {
	cmd := exec.Command("security", "find-generic-password", "-s", k.service, "-a", ref, "-w")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "could not be found") {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("keychain get %q: %s", ref, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimRight(stdout.String(), "\r\n"), nil
}

func (k *KeychainStore) Set(ref string, value string) error {
	if ref == "" {
		return errors.New("secret ref must not be empty")
	}
	cmd := exec.Command("security", "add-generic-password", "-U", "-s", k.service, "-a", ref, "-w", value)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("keychain set %q: %s", ref, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (k *KeychainStore) Delete(ref string) error {
	cmd := exec.Command("security", "delete-generic-password", "-s", k.service, "-a", ref)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "could not be found") {
			return nil
		}
		return fmt.Errorf("keychain delete %q: %s", ref, strings.TrimSpace(stderr.String()))
	}
	return nil
}

//go:build !darwin

package secrets

// Open selects the AES-256-GCM encrypted file store on Linux and Windows.
// On headless Linux servers/containers, LAGRANGIAN_MASTER_KEY can be injected
// via systemd EnvironmentFile or container secrets without requiring a GUI keyring.
func Open(fallbackDir string) (Store, error) {
	return NewFileStore(fallbackDir)
}

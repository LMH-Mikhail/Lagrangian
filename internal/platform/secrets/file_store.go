package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// FileStore stores secrets encrypted with AES-256-GCM in a user-private file.
// It is the first-class credential backend for headless Linux servers/containers
// and also serves as fallback or test backend on macOS and Windows.
type FileStore struct {
	mu      sync.Mutex
	dir     string
	vault   string
	keyFile string
}

// NewFileStore creates an AES-256-GCM encrypted secret store under dir.
func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create secrets dir: %w", err)
	}
	return &FileStore{
		dir:     dir,
		vault:   filepath.Join(dir, "vault.json"),
		keyFile: filepath.Join(dir, ".master.key"),
	}, nil
}

func (f *FileStore) BackendName() string {
	return "encrypted_file"
}

func (f *FileStore) Get(ref string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	records, err := f.readVault()
	if err != nil {
		return "", err
	}
	ciphertextB64, ok := records[ref]
	if !ok {
		return "", ErrNotFound
	}
	key, err := f.loadOrCreateKey()
	if err != nil {
		return "", err
	}
	return decryptValue(key, ciphertextB64)
}

func (f *FileStore) Set(ref string, value string) error {
	if ref == "" {
		return errors.New("secret ref must not be empty")
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	key, err := f.loadOrCreateKey()
	if err != nil {
		return err
	}
	records, err := f.readVault()
	if err != nil {
		return err
	}
	enc, err := encryptValue(key, value)
	if err != nil {
		return err
	}
	records[ref] = enc
	return f.writeVault(records)
}

func (f *FileStore) Delete(ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	records, err := f.readVault()
	if err != nil {
		return err
	}
	if _, ok := records[ref]; !ok {
		return nil
	}
	delete(records, ref)
	return f.writeVault(records)
}

func (f *FileStore) loadOrCreateKey() ([]byte, error) {
	if envKey := os.Getenv("LAGRANGIAN_MASTER_KEY"); envKey != "" {
		sum := sha256.Sum256([]byte(envKey))
		return sum[:], nil
	}
	if raw, err := os.ReadFile(f.keyFile); err == nil {
		decoded, decErr := hex.DecodeString(string(raw))
		if decErr == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}
	if err := os.WriteFile(f.keyFile, []byte(hex.EncodeToString(key)), 0o600); err != nil {
		return nil, fmt.Errorf("write master key: %w", err)
	}
	return key, nil
}

func (f *FileStore) readVault() (map[string]string, error) {
	data, err := os.ReadFile(f.vault)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	var records map[string]string
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("parse secret vault: %w", err)
	}
	if records == nil {
		records = map[string]string{}
	}
	return records, nil
}

func (f *FileStore) writeVault(records map[string]string) error {
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	tmp := f.vault + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.vault)
}

func encryptValue(key []byte, plaintext string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func decryptValue(key []byte, ciphertextB64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(raw) < nonceSize {
		return "", errors.New("corrupted secret payload")
	}
	plain, err := gcm.Open(nil, raw[:nonceSize], raw[nonceSize:], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt secret: %w", err)
	}
	return string(plain), nil
}

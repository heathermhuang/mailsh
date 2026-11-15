package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Credentials are what `mail.sh auth` saves: the OAuth client it registered and the tokens for it.
type Credentials struct {
	API          string `json:"api"`
	TokenURL     string `json:"token_url"`
	ClientID     string `json:"client_id"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresAt    int64  `json:"expires_at"`
}

const keychainService = "mail.sh"

var errNoCredentials = errors.New("not signed in: run `mail.sh auth`")

// Saved in the macOS keychain; elsewhere in a file only the user can read (~/.config/mailsh/credentials.json, mode 0600).
func useKeychain() bool {
	if os.Getenv("MAILSH_CREDENTIALS_FILE") != "" || runtime.GOOS != "darwin" {
		return false
	}
	_, err := exec.LookPath("security")
	return err == nil
}

func credentialsFile() (string, error) {
	if p := os.Getenv("MAILSH_CREDENTIALS_FILE"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mailsh", "credentials.json"), nil
}

func saveCredentials(c *Credentials) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if useKeychain() {
		// -U updates an existing item. The secret is passed as an argument, which other local processes of this
		// user can see for an instant; the keychain item itself is readable only by this user.
		return exec.Command("security", "add-generic-password", "-U", "-s", keychainService, "-a", c.API, "-w", string(data)).Run()
	}
	path, err := credentialsFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func loadCredentials(api string) (*Credentials, error) {
	var raw []byte
	if useKeychain() {
		out, err := exec.Command("security", "find-generic-password", "-s", keychainService, "-a", api, "-w").Output()
		if err != nil {
			return nil, errNoCredentials
		}
		raw = []byte(strings.TrimSpace(string(out)))
	} else {
		path, err := credentialsFile()
		if err != nil {
			return nil, err
		}
		raw, err = os.ReadFile(path)
		if err != nil {
			return nil, errNoCredentials
		}
	}
	var c Credentials
	if err := json.Unmarshal(raw, &c); err != nil || c.AccessToken == "" || c.API != api {
		return nil, errNoCredentials
	}
	return &c, nil
}

func deleteCredentials(api string) error {
	if useKeychain() {
		_ = exec.Command("security", "delete-generic-password", "-s", keychainService, "-a", api).Run()
		return nil
	}
	path, err := credentialsFile()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

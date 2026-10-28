//go:build !windows

package providers

import "errors"

// dpapiProtector is the Windows DPAPI backup protector; on other platforms
// protecting fails so callers surface "unsupported" instead of writing
// plaintext backups.
type dpapiProtector struct{}

func (dpapiProtector) Protect(plaintext []byte) ([]byte, error) {
	return nil, errors.New("backup encryption (DPAPI) is only supported on windows")
}

func (dpapiProtector) Unprotect(ciphertext []byte) ([]byte, error) {
	return nil, errors.New("backup decryption (DPAPI) is only supported on windows")
}

//go:build !windows

package providers

import "errors"

// win32Registry is the Windows registry backend; on other platforms it always
// fails so callers surface "unsupported" instead of silently losing writes.
type win32Registry struct{}

func (win32Registry) enumerate(root string) (map[string]string, error) {
	return nil, errors.New("persistent environment access is only supported on windows")
}

func (win32Registry) query(root, name string) (string, bool, error) {
	return "", false, errors.New("persistent environment access is only supported on windows")
}

func (win32Registry) set(root, name, value string) error {
	return errors.New("persistent environment access is only supported on windows")
}

func (win32Registry) delete(root, name string) error {
	return errors.New("persistent environment access is only supported on windows")
}

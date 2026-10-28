//go:build !windows

package providers

import "os"

// readConsoleSecret has no raw-console implementation outside Windows: the
// console echo there is owned by the tty line discipline and stdlib-only
// builds cannot take it over portably. The caller falls back to an ordinary
// line read; the wizard transcript still never writes the raw value to any
// output stream (confirmations use MaskSecret).
func readConsoleSecret(f *os.File, echo *secretEcho) (string, error) {
	return "", errNotConsole
}

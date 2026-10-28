package integrations

import "testing"

// FIX-14 / F-01 of the confirmation seat: isUNCPathToken tested only the `\\` and `//`
// PREFIXES, so a token whose first two bytes were both separators in a mixed spelling
// (/\host\share, \/host/share, /\\host) was classified as local. hookCommandLauncherPath
// then claimed it (leading separator = absolute, basename on the ladder) and
// hookLiveCommandPosture called os.Stat on it, which on Windows opens an SMB connection
// and hands the victim's NTLM challenge/response to an attacker-chosen host.

// TestUNCPredicateRejectsEveryMixedSeparatorSpelling pins the predicate itself, so the
// refusal is exercised on every host and not only where filepath.Base happens to keep a
// backslash. The negative half is the other direction of the same rule: a drive-letter
// path and a POSIX path with ONE leading separator must stay classifiable, which is why
// the predicate counts leading separators instead of normalising separators globally.
func TestUNCPredicateRejectsEveryMixedSeparatorSpelling(t *testing.T) {
	network := []string{
		`\\attacker.example\share\agy-tokens.bat`,
		`//attacker.example/share/agy-tokens`,
		`\\?\C:\launchers\agy-tokens.exe`,
		`\\.\C:\launchers\agy-tokens.bat`,
		`/\attacker.example\share\agy-tokens.bat`,
		`\/attacker.example/share/agy-tokens.bat`,
		`/\\attacker.example\share\agy-tokens.bat`,
		`\\/attacker.example\share\agy-tokens.bat`,
		`\/\\attacker.example\share\agy-tokens.bat`,
		`////attacker.example/share/agy-tokens`,
	}
	for _, token := range network {
		if !isUNCPathToken(token) {
			t.Fatalf("isUNCPathToken(%q) = false, want true (two leading separators, in any mixture, are a network root)", token)
		}
	}

	local := []string{
		`C:\Users\me\.gemini\antigravity-cli\bin\agy-tokens.bat`,
		`c:/Users/me/.local/bin/agy-tokens`,
		`/home/me/.local/bin/agy-tokens`,
		`/`,
		`\`,
		`agy-tokens --hook`,
		`.\agy-tokens.bat`,
		``,
	}
	for _, token := range local {
		if isUNCPathToken(token) {
			t.Fatalf("isUNCPathToken(%q) = true, want false (a drive letter or ONE leading separator is a local path)", token)
		}
	}
}

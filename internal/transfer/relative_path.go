package transfer

import "strings"

// portableRelative validates the archive's slash-separated logical path.
// POSIX backslashes are literal characters, not separators.
func portableRelative(p string) bool {
	if p == "" || len(p) > 4096 || strings.ContainsAny(p, "\x00\r\n\t") || strings.HasPrefix(p, "/") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func compatibleRelative(p string, windows bool) bool {
	if !portableRelative(p) {
		return false
	}
	if !windows {
		return true
	}
	for _, part := range strings.Split(p, "/") {
		if strings.ContainsAny(part, `\:<>"|?*`) || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return false
		}
		for _, r := range part {
			if r < 32 {
				return false
			}
		}
		base := strings.ToUpper(strings.TrimRight(strings.SplitN(part, ".", 2)[0], " "))
		switch base {
		case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$", "CLOCK$":
			return false
		}
		if len(base) >= 4 && len([]rune(base[3:])) == 1 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && strings.Contains("123456789¹²³", base[3:]) {
			return false
		}
	}
	return true
}

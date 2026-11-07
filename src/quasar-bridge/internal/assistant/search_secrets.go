package assistant

import (
	"bufio"
	"os"
	"strings"
)

// readStationSearchKeys is deliberately silent, fail-closed, and limited to
// the fixed root-only .env provisioned by the station owner. No shell is run.
func readStationSearchKeys(path string) map[string]string {
	out := map[string]string{}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 || info.Size() < 1 {
		return out
	}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key != "BRAVE_SEARCH_API_KEY" && key != "TAVILY_API_KEY" {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		if value == "" || len(value) > 512 || strings.ContainsAny(value, "\x00\r\n \t\"'\\;$`") {
			continue
		}
		out[key] = value
	}
	if scanner.Err() != nil {
		return map[string]string{}
	}
	return out
}

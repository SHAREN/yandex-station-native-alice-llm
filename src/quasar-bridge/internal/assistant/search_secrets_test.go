package assistant

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReadStationSearchKeysRequiresPrivateRegularFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows ACLs have no Unix 0600 mode; verified by station-side preflight")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "web-search.env")
	if err := os.WriteFile(p, []byte("export BRAVE_SEARCH_API_KEY=brave-test\nTAVILY_API_KEY='tavily-test'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	keys := readStationSearchKeys(p)
	if keys["BRAVE_SEARCH_API_KEY"] != "brave-test" || keys["TAVILY_API_KEY"] != "tavily-test" {
		t.Fatalf("reader rejected test keys")
	}
	if err := os.Chmod(p, 0644); err != nil {
		t.Fatal(err)
	}
	if len(readStationSearchKeys(p)) != 0 {
		t.Fatal("world-readable file must be rejected")
	}
	if err := os.Chmod(p, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.env")
	if err := os.Symlink(p, link); err != nil {
		t.Fatal(err)
	}
	if len(readStationSearchKeys(link)) != 0 {
		t.Fatal("symlink must be rejected")
	}
}

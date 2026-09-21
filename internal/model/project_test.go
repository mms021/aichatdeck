package model

import "testing"

func TestNormalizeRepository(t *testing.T) {
	for _, raw := range []string{
		"https://github.com/Example/repo.git",
		"ssh://git@github.com/Example/repo.git",
		"git@github.com:Example/repo.git",
	} {
		got, err := NormalizeRepository(raw)
		if err != nil || got != "github.com/Example/repo" {
			t.Fatalf("NormalizeRepository(%q) = %q, %v", raw, got, err)
		}
	}
	if _, err := NormalizeRepository("https://github.com/example/repo?token=secret"); err == nil {
		t.Fatal("accepted remote with query")
	}
}

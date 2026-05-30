package tools_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMergeCustomConfigsAddsSetsAndExpandsAS(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not available")
	}

	root := t.TempDir()
	configRoot := filepath.Join(root, "config")
	customRoot := filepath.Join(root, "custom")

	writeJSON(t, filepath.Join(configRoot, "master", "ai", "aistudio.google.com.json"), map[string]any{
		"domains": []string{"aistudio.google.com"},
		"dns":     []string{"8.8.8.8:53"},
		"timeout": 3600,
		"ip4":     []string{},
		"ip6":     []string{},
		"cidr4":   []string{"198.51.100.0/24"},
		"cidr6":   []string{},
		"external": map[string]any{
			"domains": []string{}, "ip4": []string{}, "ip6": []string{}, "cidr4": []string{}, "cidr6": []string{},
		},
		"replace": map[string]any{"cidr4": map[string]any{}, "cidr6": map[string]any{}},
	})
	writeJSON(t, filepath.Join(customRoot, "master", "ai", "aistudio.google.com.json"), map[string]any{
		"domains": []string{"extra.example"},
		"as":      []string{"64500"},
	})
	writeJSON(t, filepath.Join(customRoot, "dexlist", "games", "battle.net.json"), map[string]any{
		"domains": []string{"battle.net"},
		"as":      []string{"AS64500"},
	})
	writeJSON(t, filepath.Join(customRoot, ".cache", "ripe", "AS64500.json"), map[string]any{
		"data": map[string]any{
			"prefixes": []map[string]string{
				{"prefix": "203.0.113.0/24"},
				{"prefix": "2001:db8::/32"},
			},
		},
	})

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(
		python,
		filepath.Join(wd, "merge-custom-configs.py"),
		"--config-root", configRoot,
		"--custom-root", customRoot,
		"--offline",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("merge-custom-configs.py failed: %v\n%s", err, output)
	}

	merged := readJSON(t, filepath.Join(configRoot, "master", "ai", "aistudio.google.com.json"))
	assertContains(t, stringsFromJSON(t, merged["domains"]), "aistudio.google.com", "extra.example")
	assertContains(t, stringsFromJSON(t, merged["as"]), "AS64500")
	assertContains(t, stringsFromJSON(t, merged["cidr4"]), "198.51.100.0/24", "203.0.113.0/24")
	assertContains(t, stringsFromJSON(t, merged["cidr6"]), "2001:db8::/32")

	added := readJSON(t, filepath.Join(configRoot, "dexlist", "games", "battle.net.json"))
	assertContains(t, stringsFromJSON(t, added["domains"]), "battle.net")
	assertContains(t, stringsFromJSON(t, added["cidr4"]), "203.0.113.0/24")
	if _, ok := added["external"].(map[string]any); !ok {
		t.Fatalf("new config should include external defaults: %#v", added)
	}
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(content, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(content, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func stringsFromJSON(t *testing.T, value any) []string {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("value is not a JSON array: %#v", value)
	}
	output := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("array item is not a string: %#v", item)
		}
		output = append(output, text)
	}
	return output
}

func assertContains(t *testing.T, values []string, wants ...string) {
	t.Helper()
	seen := map[string]bool{}
	for _, value := range values {
		seen[value] = true
	}
	for _, want := range wants {
		if !seen[want] {
			t.Fatalf("%q missing from %v", want, values)
		}
	}
}

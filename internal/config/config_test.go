package config

import "testing"

func TestLoadValidatesGitPath(t *testing.T) {
	t.Setenv("CARAVAN_GIT_PATH", "")

	cases := map[string]bool{
		"":          false,
		"/etc":      false,
		"..":        false,
		"../jobs":   false,
		"a/../../b": false,
		".":         true,
		"jobs":      true,
		"nomad/app": true,
	}
	for path, valid := range cases {
		t.Run(path, func(t *testing.T) {
			_, err := Load([]string{"--git-url", "https://example.test/r.git", "--git-path", path})
			if valid && err != nil {
				t.Fatalf("expected %q to be valid, got %v", path, err)
			}
			if !valid && err == nil {
				t.Fatalf("expected %q to be rejected", path)
			}
		})
	}
}

package config

import (
	"reflect"
	"testing"
)

func TestVolumeHostPathPrefixes(t *testing.T) {
	t.Setenv("CARAVAN_VOLUME_HOST_PATH_PREFIXES", " /mnt/data1/ , /mnt/storage,")
	c, err := Load([]string{"--git-url", "https://example.com/repo.git"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := []string{"/mnt/data1", "/mnt/storage"}
	if !reflect.DeepEqual(c.VolumeHostPathPrefixes, want) {
		t.Fatalf("got %v, want %v", c.VolumeHostPathPrefixes, want)
	}

	for _, bad := range []string{"/", "relative/path", "/mnt/../etc"} {
		_, err := Load([]string{"--git-url", "https://example.com/repo.git", "--volume-host-path-prefixes", bad})
		if err == nil {
			t.Errorf("prefix %q should be rejected", bad)
		}
	}
}

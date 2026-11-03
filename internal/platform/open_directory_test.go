package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenDirectoryStartsConfiguredCommand(t *testing.T) {
	directory := t.TempDir()
	original := directoryOpener
	opened := ""
	directoryOpener = func(target string) error {
		opened = target
		return nil
	}
	t.Cleanup(func() { directoryOpener = original })

	if err := OpenDirectory(directory); err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		t.Fatal(err)
	}
	if opened != absolute {
		t.Fatalf("opened path = %q, want %q", opened, absolute)
	}
}

func TestOpenDirectoryRejectsMissingPathAndFiles(t *testing.T) {
	if err := OpenDirectory(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("accepted a missing directory")
	}
	file := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := OpenDirectory(file); err == nil {
		t.Fatal("accepted a regular file")
	}
}

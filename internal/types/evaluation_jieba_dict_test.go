// Agent24 fork product patch A24-KB-61 (KB-LITE-01b): the headless artifact's bundled jieba dictionary.

package types

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJiebaDictDirBeside(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "WeKnora-lite")
	if got := jiebaDictDirBeside(exe); got != "" {
		t.Fatalf("no dict directory: got %q, want empty", got)
	}
	dict := filepath.Join(root, "dict")
	if err := os.MkdirAll(dict, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := jiebaDictDirBeside(exe); got != "" {
		t.Fatalf("dict directory without jieba.dict.utf8: got %q, want empty", got)
	}
	if err := os.WriteFile(filepath.Join(dict, "jieba.dict.utf8"), []byte("x 1 n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := jiebaDictDirBeside(exe); got != dict {
		t.Fatalf("got %q, want %q", got, dict)
	}
}

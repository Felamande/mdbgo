package gomdb

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileCacheSharesAndInvalidates(t *testing.T) {
	oldMax := FileCacheMaxBytes
	FileCacheMaxBytes = 64 << 20
	defer func() { FileCacheMaxBytes = oldMax }()

	path := filepath.Join(t.TempDir(), "db.mdb")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	data1 := loadCachedFileData(path, f, 5)
	f.Close()
	if string(data1) != "hello" {
		t.Fatalf("first load = %q", data1)
	}

	f, err = os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	data2 := loadCachedFileData(path, f, 5)
	f.Close()
	if len(data1) == 0 || &data1[0] != &data2[0] {
		t.Fatal("second open did not share the cached buffer")
	}

	// Change content and modification time; the next open must reload.
	if err := os.WriteFile(path, []byte("world!"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	f, err = os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	data3 := loadCachedFileData(path, f, 6)
	f.Close()
	if string(data3) != "world!" {
		t.Fatalf("modified load = %q", data3)
	}
	if &data3[0] == &data1[0] {
		t.Fatal("modified file reused a stale cached buffer")
	}
}

func TestFileCacheDisabled(t *testing.T) {
	oldMax := FileCacheMaxBytes
	FileCacheMaxBytes = 0
	defer func() { FileCacheMaxBytes = oldMax }()

	path := filepath.Join(t.TempDir(), "db.mdb")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	data1 := loadCachedFileData(path, f, 5)
	f.Close()
	if data1 != nil {
		t.Fatal("cache disabled but loadCachedFileData returned data")
	}
}

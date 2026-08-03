package gomdb

import (
	"math"
	"os"
	"sync"
	"time"
)

// FileCacheMaxBytes bounds the total size of cached database files. Repeated
// opens of the same file reuse the in-memory copy (keyed by path, size, and
// modification time), avoiding a full re-read per connection. Set to 0 to
// disable the cache.
var FileCacheMaxBytes int64 = 512 << 20

type cachedFile struct {
	data     []byte
	size     int64
	mtime    int64
	lastUsed int64
}

var (
	fileCacheMu    sync.Mutex
	fileCache      = make(map[string]*cachedFile)
	fileCacheBytes int64
)

// loadCachedFileData returns the file contents, reading and caching them on a
// miss. The returned slice is read-only and shared between handles.
func loadCachedFileData(path string, f *os.File, size int64) []byte {
	if path == "" || size <= 0 || FileCacheMaxBytes <= 0 {
		return nil
	}
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	mtime := fi.ModTime().UnixNano()
	now := time.Now().UnixNano()

	fileCacheMu.Lock()
	if e, ok := fileCache[path]; ok && e.size == size && e.mtime == mtime {
		e.lastUsed = now
		data := e.data
		fileCacheMu.Unlock()
		return data
	}
	fileCacheMu.Unlock()

	data := readAllExact(f, size)
	if data == nil {
		return nil
	}

	fileCacheMu.Lock()
	defer fileCacheMu.Unlock()
	if e, ok := fileCache[path]; ok && e.size == size && e.mtime == mtime {
		e.lastUsed = now
		return e.data
	}
	fileCache[path] = &cachedFile{data: data, size: size, mtime: mtime, lastUsed: now}
	fileCacheBytes += size
	evictCachedFilesLocked()
	return data
}

func evictCachedFilesLocked() {
	for fileCacheBytes > FileCacheMaxBytes && len(fileCache) > 0 {
		var victim string
		var oldest int64 = math.MaxInt64
		for path, e := range fileCache {
			if e.lastUsed < oldest {
				oldest = e.lastUsed
				victim = path
			}
		}
		if victim == "" {
			return
		}
		fileCacheBytes -= fileCache[victim].size
		delete(fileCache, victim)
	}
}

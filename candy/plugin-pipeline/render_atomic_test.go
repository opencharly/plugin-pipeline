package pluginpipeline

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestGenerateWriteIsAtomic locks the atomic-write contract (RCA 2026.257):
// concurrent lanes render beds into the shared pr-beds/ tree while every
// `charly check run` loads the WHOLE project. A truncating os.WriteFile lets a
// reader observe a 0-byte/partial bed -> the entity index loses it -> "no
// entity" under parallelism. The temp+rename write must never expose a partial
// file, and must leave no ".tmp" behind.
func TestGenerateWriteIsAtomic(t *testing.T) {
	workdir := t.TempDir()
	rc := &runCtx{workdir: workdir, env: map[string]string{}}

	// large, distinct payloads: a short file is an unmistakable partial read
	const n = 1 << 20
	payloads := []string{
		strings.Repeat("A", n),
		strings.Repeat("B", n),
		strings.Repeat("C", n),
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// the writer camp: repaint the SAME out repeatedly
	for _, p := range payloads {
		wg.Add(1)
		go func(body string) {
			defer wg.Done()
			raw := map[string]any{"template": body, "out": "out/artifact.txt"}
			for {
				select {
				case <-stop:
					return
				default:
					if err := rc.runGenerate(raw); err != nil {
						t.Errorf("generate: %v", err)
						return
					}
				}
			}
		}(p)
	}

	// the reader camp: a concurrent project load must never see a torn bed
	done := make(chan struct{})
	go func() {
		defer close(done)
		out := filepath.Join(workdir, "out", "artifact.txt")
		for i := 0; i < 20000; i++ {
			b, err := os.ReadFile(out)
			if err != nil {
				continue // not created yet
			}
			for _, p := range payloads {
				if string(b) == p {
					goto next
				}
			}
			if len(b) != 0 {
				t.Errorf("torn read: %d bytes (partial write observed)", len(b))
			}
		next:
		}
	}()

	<-done
	close(stop)
	wg.Wait()

	// no temp file may survive a completed render
	if m, _ := filepath.Glob(filepath.Join(workdir, "out", "*.tmp*")); len(m) != 0 {
		t.Fatalf("temp file left behind: %v", m)
	}
}

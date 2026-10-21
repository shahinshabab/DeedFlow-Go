package pmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestAgainstPmd2json compares the port with the C++ helper over a directory
// of files: PMD_CORPUS is the directory, PMD2JSON the helper's path.
func TestAgainstPmd2json(t *testing.T) {
	dir, bin := os.Getenv("PMD_CORPUS"), os.Getenv("PMD2JSON")
	if dir == "" || bin == "" {
		t.Skip("PMD_CORPUS and PMD2JSON not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	bad := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bin, f)
		var out bytes.Buffer
		cmd.Stdout = &out
		rc := 0
		if err := cmd.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				rc = ee.ExitCode() // -1 when killed by a signal
			} else {
				t.Fatal(err)
			}
		}
		calls, res := Read(data)
		gotRC := 0
		switch res {
		case Unsupported:
			gotRC = 3
		case Failed:
			gotRC = 4
		}
		var got []byte
		if res == OK || res == Failed {
			got = JSON(calls)
		}
		want := out.Bytes()
		if rc == 3 || rc < 0 {
			want = nil
		}
		if gotRC != rc || !bytes.Equal(got, want) {
			bad++
			if bad <= 5 {
				gl, wl := bytes.Split(got, []byte("\n")), bytes.Split(want, []byte("\n"))
				for i := 0; i < len(gl) || i < len(wl); i++ {
					var a, b []byte
					if i < len(gl) {
						a = gl[i]
					}
					if i < len(wl) {
						b = wl[i]
					}
					if !bytes.Equal(a, b) {
						t.Errorf("%s: rc go=%d c++=%d line %d\n go: %.900s\n c++: %.900s", filepath.Base(f), gotRC, rc, i, a, b)
						break
					}
				}
			}
		}
	}
	t.Logf("%d files, %d differ", len(files), bad)
	if bad > 0 {
		t.Fail()
	}
}

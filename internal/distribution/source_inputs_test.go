package distribution

import "testing"

func TestSourceModuleRefusesFilesystemReplacementInputs(t *testing.T) {
	for _, tc := range []struct {
		input string
		ok    bool
	}{
		{"module fixture.invalid/source\nrequire example.invalid/lib v1.0.0\n", true},
		{"replace example.invalid/lib => example.invalid/fork v1.2.3\n", true},
		{"// replace example.invalid/lib => ../private\n", true},
		{"replace (\nexample.invalid/lib => example.invalid/fork v1.2.3\n)\n", true},
		{"replace example.invalid/lib => ../private\n", false},
		{"replace example.invalid/lib => ./local\n", false},
		{"replace example.invalid/lib => /private/path\n", false},
		{"replace example.invalid/lib => \"../private path\"\n", false},
		{"replace (\nexample.invalid/lib v1.0.0 => \"./local\"\n)\n", false},
		{"replace example.invalid/lib => C:\\private\n", false},
	} {
		if err := checkSourceModule([]byte(tc.input)); (err == nil) != tc.ok {
			t.Errorf("source module input=%q accepted=%v want=%v: %v", tc.input, err == nil, tc.ok, err)
		}
	}
}

func TestSourceSymlinksMustResolveWithinTrackedInputs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []sourceFile
		ok    bool
	}{
		{"tracked-file", []sourceFile{{name: "src/file.go", mode: "100644"}, {name: "link.go", mode: "120000", data: []byte("src/file.go")}}, true},
		{"tracked-directory", []sourceFile{{name: "src/file.go", mode: "100644"}, {name: "link", mode: "120000", data: []byte("src")}}, true},
		{"chain", []sourceFile{{name: "src/file.go", mode: "100644"}, {name: "alias", mode: "120000", data: []byte("src")},
			{name: "link.go", mode: "120000", data: []byte("alias/file.go")}}, true},
		{"untracked-target", []sourceFile{{name: "link.go", mode: "120000", data: []byte("untracked-private.go")}}, false},
		{"cycle", []sourceFile{{name: "first", mode: "120000", data: []byte("second")}, {name: "second", mode: "120000", data: []byte("first")}}, false},
		{"file-parent", []sourceFile{{name: "file.go", mode: "100644"}, {name: "link", mode: "120000", data: []byte("file.go/child")}}, false},
		{"symlink-parent-escape", []sourceFile{{name: "inner/file.go", mode: "100644"}, {name: "inner/back", mode: "120000", data: []byte("..")},
			{name: "link", mode: "120000", data: []byte("inner/back/../private.go")}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkSourceLinks(tc.files); (err == nil) != tc.ok {
				t.Fatal("source link closure differs", err, tc.ok)
			}
		})
	}
}

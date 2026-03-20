package clistate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetString_PrefersStoredValue(t *testing.T) {
	store := newTestStore(t, "app1")
	if err := store.PersistString("name", "Alice"); err != nil {
		t.Fatalf("persist string: %v", err)
	}

	if got := store.GetString("name", "fallback"); got != "Alice" {
		t.Fatalf("GetString = %q, want %q", got, "Alice")
	}
}

func TestGetString_UsesOverride(t *testing.T) {
	store := newTestStore(t, "app1")
	if err := store.PersistString("name", "Alice"); err != nil {
		t.Fatalf("persist string: %v", err)
	}

	override := "Bob"
	if got := store.GetString("name", "fallback", &override); got != "Bob" {
		t.Fatalf("GetString override = %q, want %q", got, "Bob")
	}
}

func TestGetInt_PrefersStoredValue(t *testing.T) {
	store := newTestStore(t, "app1")
	if err := store.PersistInt("count", 7); err != nil {
		t.Fatalf("persist int: %v", err)
	}

	if got := store.GetInt("count", 1); got != 7 {
		t.Fatalf("GetInt = %d, want %d", got, 7)
	}
}

func TestGetBool_PrefersStoredValue(t *testing.T) {
	store := newTestStore(t, "app1")
	if err := store.PersistBool("enabled", true); err != nil {
		t.Fatalf("persist bool: %v", err)
	}

	if got := store.GetBool("enabled", false); !got {
		t.Fatalf("GetBool = %v, want true", got)
	}
}

func TestGetStruct_RoundTripsPersistedValue(t *testing.T) {
	type profile struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}

	store := newTestStore(t, "app1")
	want := profile{Name: "Alice", Age: 42}
	if err := store.PersistStruct("profile", want); err != nil {
		t.Fatalf("persist struct: %v", err)
	}

	var got profile
	if ok := store.GetStruct("profile", &got); !ok {
		t.Fatalf("GetStruct returned false")
	}
	if got != want {
		t.Fatalf("GetStruct = %+v, want %+v", got, want)
	}
}

func TestGetProjectDir_ReturnsModuleDirForMatchingApp(t *testing.T) {
	moduleDir := writeTempModule(t, "github.com/example/app1")
	nestedDir := filepath.Join(moduleDir, "cmd", "app1")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested dir: %v", err)
	}

	chdir(t, nestedDir)

	store := newTestStore(t, "app1")
	projectDir := store.GetProjectDir()
	want := canonicalPath(t, moduleDir)

	if got := canonicalPath(t, projectDir); got != want {
		t.Fatalf("project dir = %q, want %q", got, want)
	}

	if got := canonicalPath(t, store.GetString("project_dir", "")); got != want {
		t.Fatalf("stored project_dir = %q, want %q", got, want)
	}
}

func TestGetProjectDir_ReturnsModuleDirForGoPrefixedModule(t *testing.T) {
	moduleDir := writeTempModule(t, "github.com/example/go-app2")
	chdir(t, moduleDir)

	store := newTestStore(t, "app2")
	projectDir := store.GetProjectDir()
	want := canonicalPath(t, moduleDir)

	if got := canonicalPath(t, projectDir); got != want {
		t.Fatalf("project dir = %q, want %q", got, want)
	}
}

func TestGetProjectDir_FallsBackToStoredProjectDir(t *testing.T) {
	chdir(t, t.TempDir())

	store := newTestStore(t, "app1")
	want := filepath.Join(t.TempDir(), "stored-project")
	if err := store.PersistString("project_dir", want); err != nil {
		t.Fatalf("persist project_dir: %v", err)
	}

	if got := store.GetProjectDir(); got != want {
		t.Fatalf("project dir = %q, want %q", got, want)
	}
}

func newTestStore(t *testing.T, app string) *Store {
	t.Helper()

	return &Store{
		app:  app,
		path: filepath.Join(t.TempDir(), "config.json"),
		data: make(map[string]any),
	}
}

func writeTempModule(t *testing.T, modulePath string) string {
	t.Helper()

	moduleDir := t.TempDir()
	goModPath := filepath.Join(moduleDir, "go.mod")
	goMod := "module " + modulePath + "\n\ngo 1.24.0\n"

	if err := os.WriteFile(goModPath, []byte(goMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	return moduleDir
}

func chdir(t *testing.T, dir string) {
	t.Helper()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %q: %v", dir, err)
	}

	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})
}

func canonicalPath(t *testing.T, path string) string {
	t.Helper()

	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		path = resolved
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("abs %q: %v", path, err)
	}

	return abs
}

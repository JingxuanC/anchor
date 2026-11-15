package core

import "testing"

func initTest(t *testing.T) *Store {
	t.Helper()
	return initAt(t, t.TempDir())
}

func initAt(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := InitWorkspace(dir, "ws-test", "dev-test")
	if err != nil {
		t.Fatalf("init workspace %s: %v", dir, err)
	}
	return s
}

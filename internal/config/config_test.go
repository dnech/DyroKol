package config

import (
	"path/filepath"
	"testing"
)

func TestDirUsesXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join("tmp", "xdg"))
	got, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("tmp", "xdg", "dyrokol")
	if got != want {
		t.Fatalf("Dir() = %q, want %q", got, want)
	}
}

func TestDirFallsBackToHomeConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("USERPROFILE", filepath.Join("tmp", "home"))
	t.Setenv("HOME", filepath.Join("tmp", "home"))
	got, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("tmp", "home", ".config", "dyrokol")
	if got != want {
		t.Fatalf("Dir() = %q, want %q", got, want)
	}
}

func TestResolveOrder(t *testing.T) {
	t.Setenv("DYROKOL_TESTKEY", "env-value")

	if got := Resolve("flag", "TESTKEY", "def"); got != "flag" {
		t.Fatalf("flag wins: %q", got)
	}
	if got := Resolve("", "TESTKEY", "def"); got != "env-value" {
		t.Fatalf("env beats default: %q", got)
	}
	t.Setenv("DYROKOL_TESTKEY", "")
	if got := Resolve("", "TESTKEY", "def"); got != "def" {
		t.Fatalf("default fallback: %q", got)
	}
}

func TestEnvSet(t *testing.T) {
	t.Setenv("DYROKOL_TESTKEY2", "x")
	if !EnvSet("TESTKEY2") {
		t.Fatal("expected set")
	}
	t.Setenv("DYROKOL_TESTKEY2", "")
	if EnvSet("TESTKEY2") {
		t.Fatal("empty value should not count")
	}
}

func TestListenAddr(t *testing.T) {
	for in, want := range map[string]string{
		"9181":           "127.0.0.1:9181",
		" 9182 ":         "127.0.0.1:9182",
		":9181":          ":9181",
		"0.0.0.0:80":     "0.0.0.0:80",
		"127.0.0.1:9181": "127.0.0.1:9181",
		"[::1]:80":       "[::1]:80",
		"":               "",
		"70000":          "70000",
	} {
		if got := ListenAddr(in); got != want {
			t.Errorf("ListenAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

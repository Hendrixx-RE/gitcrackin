package config

import (
	"os"
	"testing"
	"time"
)

// unsetForTest removes an env var for the duration of the test and
// restores whatever value (or absence) it had beforehand. t.Setenv can
// only set a value, never unset one, so a true "nothing configured" test
// needs os.Unsetenv directly.
func unsetForTest(t *testing.T, key string) {
	t.Helper()
	prev, existed := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("os.Unsetenv(%q): %v", key, err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, prev)
		}
	})
}

func TestLoad_Defaults(t *testing.T) {
	for _, key := range []string{
		"GITCRACKIND_ADDR",
		"GITCRACKIND_READ_HEADER_TIMEOUT",
		"GITCRACKIND_READ_TIMEOUT",
		"GITCRACKIND_WRITE_TIMEOUT",
		"GITCRACKIND_IDLE_TIMEOUT",
		"GITCRACKIND_SHUTDOWN_TIMEOUT",
	} {
		unsetForTest(t, key)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.Addr != defaultAddr {
		t.Errorf("Addr = %q, want %q", cfg.Addr, defaultAddr)
	}
	if cfg.ReadHeaderTimeout != defaultReadHeaderTimeout {
		t.Errorf("ReadHeaderTimeout = %v, want %v", cfg.ReadHeaderTimeout, defaultReadHeaderTimeout)
	}
	if cfg.ReadTimeout != defaultReadTimeout {
		t.Errorf("ReadTimeout = %v, want %v", cfg.ReadTimeout, defaultReadTimeout)
	}
	if cfg.WriteTimeout != defaultWriteTimeout {
		t.Errorf("WriteTimeout = %v, want %v", cfg.WriteTimeout, defaultWriteTimeout)
	}
	if cfg.IdleTimeout != defaultIdleTimeout {
		t.Errorf("IdleTimeout = %v, want %v", cfg.IdleTimeout, defaultIdleTimeout)
	}
	if cfg.ShutdownTimeout != defaultShutdownTimeout {
		t.Errorf("ShutdownTimeout = %v, want %v", cfg.ShutdownTimeout, defaultShutdownTimeout)
	}
}

func TestLoad_Overrides(t *testing.T) {
	t.Setenv("GITCRACKIND_ADDR", ":9090")
	t.Setenv("GITCRACKIND_READ_HEADER_TIMEOUT", "2s")
	t.Setenv("GITCRACKIND_READ_TIMEOUT", "3s")
	t.Setenv("GITCRACKIND_WRITE_TIMEOUT", "4s")
	t.Setenv("GITCRACKIND_IDLE_TIMEOUT", "5s")
	t.Setenv("GITCRACKIND_SHUTDOWN_TIMEOUT", "6s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.Addr != ":9090" {
		t.Errorf("Addr = %q, want %q", cfg.Addr, ":9090")
	}
	if cfg.ReadHeaderTimeout != 2*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 2s", cfg.ReadHeaderTimeout)
	}
	if cfg.ShutdownTimeout != 6*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 6s", cfg.ShutdownTimeout)
	}
}

func TestLoad_InvalidDuration(t *testing.T) {
	t.Setenv("GITCRACKIND_READ_HEADER_TIMEOUT", "banana")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() with an invalid duration should fail fast, got nil error")
	}
}

func TestLoad_EmptyAddrRejected(t *testing.T) {
	t.Setenv("GITCRACKIND_ADDR", "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() with an empty GITCRACKIND_ADDR should fail, got nil error")
	}
}

func TestLoad_NonPositiveReadHeaderTimeoutRejected(t *testing.T) {
	t.Setenv("GITCRACKIND_READ_HEADER_TIMEOUT", "0s")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() with a zero ReadHeaderTimeout should fail, got nil error")
	}
}

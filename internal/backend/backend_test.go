package backend

import (
	"io"
	"testing"
	"time"
)

func TestNewDefaultsToArchBackend(t *testing.T) {
	t.Setenv("DOTPKG_BACKEND", "")
	value, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := value.(*Arch); !ok {
		t.Fatalf("backend type = %T, want *Arch", value)
	}
}

func TestNewRejectsUnsupportedBackend(t *testing.T) {
	t.Setenv("DOTPKG_BACKEND", "fedora")
	if _, err := New(); err == nil {
		t.Fatal("unsupported backend was accepted")
	}
}

func TestArchConfigureAppliesOperationalOptions(t *testing.T) {
	arch := NewArch()
	if err := arch.Configure(Options{
		HTTPTimeout:   2 * time.Second,
		AURRetries:    5,
		AURRetryDelay: 20 * time.Millisecond,
		Logger:        io.Discard,
	}); err != nil {
		t.Fatal(err)
	}
	if arch.HTTPClient.Timeout != 2*time.Second || arch.AURRetries != 5 || arch.AURRetryDelay != 20*time.Millisecond {
		t.Fatalf("configuration = %#v", arch)
	}
}

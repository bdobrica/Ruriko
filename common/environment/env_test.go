package environment_test

import (
	"testing"
	"time"

	"github.com/bdobrica/Ruriko/common/environment"
)

func TestStringOr(t *testing.T) {
	t.Setenv("TEST_STRING", "hello")
	if got := environment.StringOr("TEST_STRING", "default"); got != "hello" {
		t.Errorf("expected %q, got %q", "hello", got)
	}
	if got := environment.StringOr("TEST_STRING_MISSING", "default"); got != "default" {
		t.Errorf("expected %q, got %q", "default", got)
	}
}

func TestRequiredString(t *testing.T) {
	t.Setenv("TEST_REQUIRED", "value")
	v, err := environment.RequiredString("TEST_REQUIRED")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "value" {
		t.Errorf("expected %q, got %q", "value", v)
	}

	_, err = environment.RequiredString("TEST_REQUIRED_MISSING")
	if err == nil {
		t.Error("expected error for missing variable, got nil")
	}
}

func TestBoolOr(t *testing.T) {
	t.Setenv("TEST_BOOL", "true")
	if !environment.BoolOr("TEST_BOOL", false) {
		t.Error("expected true")
	}
	t.Setenv("TEST_BOOL", "0")
	if environment.BoolOr("TEST_BOOL", true) {
		t.Error("expected false")
	}
	if !environment.BoolOr("TEST_BOOL_MISSING", true) {
		t.Error("expected default true")
	}
}

func TestIntOr(t *testing.T) {
	t.Setenv("TEST_INT", "42")
	if got := environment.IntOr("TEST_INT", 0); got != 42 {
		t.Errorf("expected 42, got %d", got)
	}
	if got := environment.IntOr("TEST_INT_MISSING", 99); got != 99 {
		t.Errorf("expected 99, got %d", got)
	}
	t.Setenv("TEST_INT_BAD", "notanint")
	if got := environment.IntOr("TEST_INT_BAD", 7); got != 7 {
		t.Errorf("expected default 7 for bad value, got %d", got)
	}
}

func TestDurationOr(t *testing.T) {
	t.Setenv("TEST_DUR", "30s")
	if got := environment.DurationOr("TEST_DUR", time.Minute); got != 30*time.Second {
		t.Errorf("expected 30s, got %v", got)
	}
	if got := environment.DurationOr("TEST_DUR_MISSING", time.Minute); got != time.Minute {
		t.Errorf("expected 1m, got %v", got)
	}
}

func TestStringSliceOr(t *testing.T) {
	t.Setenv("TEST_SLICE", "a, b , c")
	got := environment.StringSliceOr("TEST_SLICE", nil)
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("unexpected result: %v", got)
	}
	fallback := []string{"x"}
	if got := environment.StringSliceOr("TEST_SLICE_MISSING", fallback); len(got) != 1 || got[0] != "x" {
		t.Errorf("expected fallback, got %v", got)
	}
}

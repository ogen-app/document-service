package config

import "testing"

func TestLoad_RejectsMemoryRatioAboveOne(t *testing.T) {
	t.Setenv("DOCUMENTS_SERVICE_MEMORY_LIMIT_RATIO", "1.5")
	if _, err := Load(); err == nil {
		t.Fatal("expected an error for MEMORY_LIMIT_RATIO > 1")
	}
}

func TestLoad_AcceptsValidMemoryRatio(t *testing.T) {
	t.Setenv("DOCUMENTS_SERVICE_MEMORY_LIMIT_RATIO", "0.85")
	if _, err := Load(); err != nil {
		t.Fatalf("valid ratio rejected: %v", err)
	}
}

func TestLoad_NormalisesBarePort(t *testing.T) {
	t.Setenv("DOCUMENTS_SERVICE_LISTEN", "50051")
	c, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Listen != ":50051" {
		t.Fatalf("bare port not normalised: %q", c.Listen)
	}
}

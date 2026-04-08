package awscfg

import (
	"context"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(context.Background(), "", "", "", "")
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	_ = cfg
}

func TestLoadWithRegion(t *testing.T) {
	cfg, err := Load(context.Background(), "us-west-2", "", "", "")
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if cfg.Region != "us-west-2" {
		t.Errorf("Region = %q, want us-west-2", cfg.Region)
	}
}

func TestLoadWithEnvCreds(t *testing.T) {
	t.Setenv("TEST_AWSCFG_AKID", "TESTACCESSKEY")
	t.Setenv("TEST_AWSCFG_SECRET", "TESTSECRETKEY")

	cfg, err := Load(context.Background(), "us-east-1", "",
		"TEST_AWSCFG_AKID", "TEST_AWSCFG_SECRET")
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	creds, err := cfg.Credentials.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("Retrieve() = %v", err)
	}
	if creds.AccessKeyID != "TESTACCESSKEY" {
		t.Errorf("AccessKeyID = %q, want TESTACCESSKEY", creds.AccessKeyID)
	}
	if creds.SecretAccessKey != "TESTSECRETKEY" {
		t.Errorf("SecretAccessKey = %q, want TESTSECRETKEY", creds.SecretAccessKey)
	}
}

func TestLoadEnvVarsEmpty(t *testing.T) {
	_, err := Load(context.Background(), "us-east-1", "",
		"NONEXISTENT_AKID_VAR", "NONEXISTENT_SECRET_VAR")
	if err != nil {
		t.Fatalf("Load() should not fail with unset env vars: %v", err)
	}
}

func TestLoadSetsHTTPTimeout(t *testing.T) {
	cfg, err := Load(context.Background(), "us-east-1", "", "", "")
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	// The HTTP client should be set (non-nil).
	if cfg.HTTPClient == nil {
		t.Error("HTTPClient should be set with timeout")
	}
}

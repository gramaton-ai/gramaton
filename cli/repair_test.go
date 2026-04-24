package cli

import "testing"

// TestRepairContentQualityFlagRegistered protects the CLI wiring
// for the --content-quality flag. If a refactor accidentally drops
// the `repairCmd.Flags().BoolVar(&repairContentQuality, ...)` line
// in init(), this test catches it. The self-heal itself is covered
// by curation/self_heal_test.go — this test only proves the flag
// exists and the boolean is wired.
func TestRepairContentQualityFlagRegistered(t *testing.T) {
	f := repairCmd.Flags().Lookup("content-quality")
	if f == nil {
		t.Fatal("content-quality flag not registered on repairCmd")
	}
	if f.Value.Type() != "bool" {
		t.Errorf("content-quality flag type = %q, want bool", f.Value.Type())
	}
}

func TestRepairDryRunFlagStillRegistered(t *testing.T) {
	// Sanity: the --dry-run flag should remain after we added
	// --content-quality alongside it (regression test for an
	// init-function rewrite accidentally dropping the older flag).
	if repairCmd.Flags().Lookup("dry-run") == nil {
		t.Fatal("dry-run flag disappeared from repairCmd")
	}
}

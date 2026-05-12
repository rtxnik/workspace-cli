package xray

import "testing"

func TestAtomicSwapSymlink(t *testing.T)            { t.Skip("pending Plan 22-03 atomic symlink swap") }
func TestValidationGate(t *testing.T)               { t.Skip("pending Plan 22-03 xray -test validation gate") }
func TestManualRecoveryOnFailedSwitch(t *testing.T) { t.Skip("pending Plan 22-03 manual-recovery (TRIPWIRE: must assert no auto-rollback)") }

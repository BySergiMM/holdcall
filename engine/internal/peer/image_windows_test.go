//go:build windows

package peer

import "testing"

// There is no implementation on Windows, and the contract is that callers get
// an error rather than a zero Image they might mistake for an answer. This
// test never runs anywhere today -- nothing in this project has ever run on
// Windows -- but it type-checks under `GOOS=windows go vet` and states what
// the stubs are for.
func TestWindowsReportsUnsupportedRatherThanAnEmptyIdentity(t *testing.T) {
	if img, err := ImageOf(1); err == nil {
		t.Errorf("ImageOf returned no error; got %+v", img)
	}
	if img, err := ImageOfFile(`C:\Windows\System32\cmd.exe`); err == nil {
		t.Errorf("ImageOfFile returned no error; got %+v", img)
	}
	if ppid, err := ParentOf(1); err == nil {
		t.Errorf("ParentOf returned no error; got %d", ppid)
	}
	if supported, _ := IsSelf(nil); supported {
		t.Error("IsSelf reported the platform as supported")
	}
}

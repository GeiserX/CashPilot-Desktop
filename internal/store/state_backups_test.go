package store

import (
	"testing"
	"time"
)

func TestStateBackupConfirmations(t *testing.T) {
	s := openTestStore(t)

	if c, err := s.StateBackup("mysterium"); err != nil || c != nil {
		t.Fatalf("an unconfirmed service gave %+v, %v", c, err)
	}
	at := time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)
	if err := s.ConfirmStateBackup("mysterium", "aaaa", "manual", at); err != nil {
		t.Fatal(err)
	}
	c, err := s.StateBackup("mysterium")
	if err != nil || c == nil {
		t.Fatalf("StateBackup = %+v, %v", c, err)
	}
	if c.StateID != "aaaa" || c.Method != "manual" || !c.ConfirmedAt.Equal(at) {
		t.Errorf("confirmation = %+v", c)
	}
	// A later export replaces it.
	if err := s.ConfirmStateBackup("mysterium", "bbbb", "export", at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.StateBackup("mysterium"); c == nil || c.StateID != "bbbb" || c.Method != "export" {
		t.Errorf("after export = %+v", c)
	}
	if err := s.ClearStateBackup("mysterium"); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.StateBackup("mysterium"); c != nil {
		t.Errorf("undo left %+v", c)
	}
}

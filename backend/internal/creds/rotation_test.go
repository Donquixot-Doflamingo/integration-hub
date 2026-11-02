package creds

import (
	"errors"
	"testing"
	"time"
)

func TestRotationSteps(t *testing.T) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	a := AccountSecrets{Active: Slot{Version: 3, Values: map[string]string{"passcode": "old-passcode"}}}
	next := NewSlot(a, map[string]string{"passcode": "new-passcode"}, nil, now)
	if next.Version != 4 || len(next.Fingerprint) != len("sha256:")+6 {
		t.Fatalf("new slot %+v", next)
	}
	a, err := AddNext(a, next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Promote(a, 5); !errors.Is(err, ErrRotationConflict) {
		t.Fatalf("promote with a stale expected version: %v", err)
	}
	a, err = Promote(a, 4)
	if err != nil {
		t.Fatal(err)
	}
	if a.Active.Version != 4 || a.Next != nil || a.Previous == nil || a.Previous.Version != 3 {
		t.Fatalf("after promote %+v", a)
	}
	if _, err := Promote(a, 4); !errors.Is(err, ErrRotationConflict) {
		t.Fatalf("second promote: %v", err)
	}
	if v := a.Views(); len(v) != 2 || v[0].Slot != "active" || v[1].Slot != "previous" {
		t.Fatalf("views %+v", v)
	}
	if _, err := DeleteOld(a, 2); !errors.Is(err, ErrRotationConflict) {
		t.Fatalf("delete with a stale expected version: %v", err)
	}
	a, err = DeleteOld(a, 3)
	if err != nil || a.Previous != nil {
		t.Fatalf("after delete %+v %v", a, err)
	}
	if NewSlot(a, nil, nil, now).Version != 5 {
		t.Fatal("versions are never reused")
	}
	if _, err := DeleteOld(a, 3); !errors.Is(err, ErrRotationConflict) {
		t.Fatalf("delete twice: %v", err)
	}
}

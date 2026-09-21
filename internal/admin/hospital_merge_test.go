package admin

import (
	"testing"

	"ambigo-backend/internal/translation"
)

func TestHospitalNamesMatch(t *testing.T) {
	match := []struct{ a, b string }{
		{"KIMS Hospital", "KIMS Hospital, Secunderabad"},
		{"KIMS - Saveera Hospital", "kims saveera"},
		{"Apollo Hospitals", "apollo"},
	}
	for _, c := range match {
		if !HospitalNamesMatch(c.a, c.b) {
			t.Errorf("expected match: %q vs %q", c.a, c.b)
		}
	}
	nomatch := []struct{ a, b string }{
		{"KIMS Hospital", "Yashoda Clinic"},
		{"Care & Cure Hospital", "City Pharmacy"},
		{"AB", "AB"},
		{"", "KIMS Hospital"},
	}
	for _, c := range nomatch {
		if HospitalNamesMatch(c.a, c.b) {
			t.Errorf("expected no match: %q vs %q", c.a, c.b)
		}
	}
}

func hospRow(id, name, placeID string) Hospital {
	return Hospital{ID: id, Name: translation.Map{"en_US": name}, PlaceID: placeID}
}

func TestPickMergeTargetPrefersPlaced(t *testing.T) {
	near := []Hospital{
		hospRow("admin-row", "KIMS Hospital", ""),
		hospRow("google-row", "KIMS Hospital, Secunderabad", "ChIJ123"),
	}
	got := PickMergeTarget(near, "KIMS Hospital")
	if got == nil || got.ID != "google-row" {
		t.Fatalf("expected google-row survivor, got %+v", got)
	}
}

func TestPickMergeTargetSkipsDifferentName(t *testing.T) {
	near := []Hospital{hospRow("other", "Yashoda Clinic", "")}
	if got := PickMergeTarget(near, "KIMS Hospital"); got != nil {
		t.Fatalf("expected nil for different name, got %+v", got)
	}
}

func TestPickMergeTargetFallsBackToAdminRow(t *testing.T) {
	near := []Hospital{hospRow("admin-row", "KIMS Hospital", "")}
	got := PickMergeTarget(near, "KIMS Hospital")
	if got == nil || got.ID != "admin-row" {
		t.Fatalf("expected admin-row survivor, got %+v", got)
	}
}

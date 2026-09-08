package admin

import (
	"regexp"
	"strings"
)

// Government / public-institution place types from the Google Places API.
var governmentGoogleTypes = map[string]bool{
	"local_government_office":     true,
	"government_office":           true,
	"national_government_office":  true,
	"city_hall":                   true,
}

var governmentNamePattern = regexp.MustCompile(`(?i)\b(government|govt|district hospital|civil hospital|rims|esi|sadar|zilla|municipal|corporation hospital|state hospital|public hospital|community health|primary health|taluk|upgraded|general hospital)\b`)
var multiSpecialityNamePattern = regexp.MustCompile(`(?i)(multi\s*special|multispecial|super\s*special|superspecial|super speciality|kims|apollo|yashoda|manipal|fortis|aiims|medanta|nims|sanjeevani|star hospital|surya)`)
var clinicNamePattern = regexp.MustCompile(`(?i)\bclinic\b|nursing home`)

// ClassifyHospitalType infers the hospital type from its name and raw Google
// place types. It is a heuristic; admins can override via the admin app.
func ClassifyHospitalType(name string, googleTypes []string) string {
	if hasGovernmentGoogleType(googleTypes) || governmentNamePattern.MatchString(name) {
		return HospitalTypeGovernment
	}
	if multiSpecialityNamePattern.MatchString(name) {
		return HospitalTypeMultiSpeciality
	}
	if clinicNamePattern.MatchString(name) {
		return HospitalTypeClinic
	}
	return HospitalTypePrivate
}

// HospitalCategoryFromType derives the app-facing category.
func HospitalCategoryFromType(hospitalType string) string {
	if hospitalType == HospitalTypeGovernment || hospitalType == HospitalTypeMultiSpeciality {
		return HospitalCategoryEmergency
	}
	return HospitalCategoryNonEmergency
}

func hasGovernmentGoogleType(types []string) bool {
	for _, t := range types {
		if governmentGoogleTypes[strings.ToLower(t)] {
			return true
		}
	}
	return false
}

// IsClinicName reports whether name matches clinic pattern.
func IsClinicName(name string) bool {
	return clinicNamePattern.MatchString(name)
}

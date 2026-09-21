package admin

import "time"

// MDAmbulance is one ambulance owned by a hospital, created by an MD.
// It holds no driver data itself; numbers live in MDAmbulanceDriver links.
type MDAmbulance struct {
	ID          string    `db:"id" json:"_id"`
	HospitalID  string    `db:"hospital_id" json:"hospital_id"`
	CreatedByMD *string   `db:"created_by_md_id" json:"created_by_md_id,omitempty"`
	Label       string    `db:"label" json:"label"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
	UpdatedAt   time.Time `db:"updated_at" json:"updated_at"`
}

// MDAmbulanceDriver links one driver mobile under one ambulance.
// Active is the only gate: one active per mobile AND one active per ambulance.
type MDAmbulanceDriver struct {
	ID           string    `db:"id" json:"_id"`
	AmbulanceID  string    `db:"ambulance_id" json:"ambulance_id"`
	DriverMobile string    `db:"driver_mobile" json:"driver_mobile"`
	Active       bool      `db:"active" json:"active"`
	CreatedAt    time.Time `db:"created_at" json:"created_at"`
	UpdatedAt    time.Time `db:"updated_at" json:"updated_at"`
}

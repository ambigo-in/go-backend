package interfaces

// DriverStatus represents the real-time availability of a driver
type DriverStatus string

const (
	StatusAvailable DriverStatus = "AVAILABLE"
	StatusBusy      DriverStatus = "BUSY"
	StatusOffline   DriverStatus = "OFFLINE"
)

// LocationStore defines the fast, in-memory contract for tracking
// driver locations and their H3 cell indexes.
type LocationStore interface {
	UpdateLocation(driverID string, lat float64, lng float64) error
	GetLocation(driverID string) (lat float64, lng float64, err error)
	GetDriversInCell(h3Cell string) ([]string, error)
	GetDriversInCells(h3Cells []string) ([]string, error)
	RemoveDriver(driverID string) error
	SetDriverStatus(driverID string, status DriverStatus) error
	GetDriverStatus(driverID string) (DriverStatus, error)
	SetDriverVehicleType(driverID, vehicleType string) error
	GetDriverVehicleType(driverID string) (string, error)
}

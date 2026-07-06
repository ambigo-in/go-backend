package location

import (
	"github.com/uber/h3-go/v4"
)

// Resolution 9 gives us hexagons that are roughly ~174 meters edge-to-edge.
// This is standard for ride-hailing applications.
const DefaultResolution = 9

// GetH3Cell converts latitude and longitude to an H3 cell index string at default resolution.
func GetH3Cell(lat float64, lng float64) string {
	return GetH3CellAtResolution(lat, lng, DefaultResolution)
}

// GetH3CellAtResolution converts lat/lng to an H3 cell at the given resolution.
func GetH3CellAtResolution(lat float64, lng float64, resolution int) string {
	latLng := h3.NewLatLng(lat, lng)
	cell, err := h3.LatLngToCell(latLng, resolution)
	if err != nil {
		return ""
	}
	return cell.String()
}

// GetNeighborCells returns the origin cell and its immediate neighbors (1-ring).
func GetNeighborCells(cellStr string) ([]string, error) {
	return GetNeighborCellsAtRing(cellStr, 1)
}

// GetNeighborCellsAtRing returns the origin cell and all neighbors within the given ring distance.
func GetNeighborCellsAtRing(cellStr string, ring int) ([]string, error) {
	cell := h3.IndexFromString(cellStr)

	neighbors, err := h3.GridDisk(h3.Cell(cell), ring)
	if err != nil {
		return nil, err
	}

	result := make([]string, 0, len(neighbors))
	for _, neighbor := range neighbors {
		result = append(result, string(neighbor.String()))
	}

	return result, nil
}

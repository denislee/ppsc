// Package metro provides the São Paulo subway network and a nearest-station
// lookup. The station list (see stations.go) is embedded from OpenStreetMap so
// the lookup works fully offline once a property has been geocoded.
package metro

import "math"

// Station is a single subway station with its line and map colour.
type Station struct {
	Name  string  `json:"name"`
	Line  string  `json:"line"`
	Color string  `json:"color"`
	Lat   float64 `json:"lat"`
	Lon   float64 `json:"lon"`
}

// Stations returns the full embedded network (Lines 1–5).
func Stations() []Station { return stations }

// Nearest returns the closest station to (lat, lon) and the great-circle
// distance to it in metres. The bool is false when no stations are loaded.
func Nearest(lat, lon float64) (Station, int, bool) {
	if len(stations) == 0 {
		return Station{}, 0, false
	}
	best := 0
	bestD := math.MaxFloat64
	for i, s := range stations {
		d := haversineMeters(lat, lon, s.Lat, s.Lon)
		if d < bestD {
			bestD, best = d, i
		}
	}
	return stations[best], int(math.Round(bestD)), true
}

// haversineMeters returns the great-circle distance between two lat/lon points.
func haversineMeters(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadiusM = 6371000.0
	rad := math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLon := (lon2 - lon1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return earthRadiusM * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// Package geo provides minimal great-circle helpers for filtering aircraft
// near a fixed observer location.
package geo

import "math"

const earthRadiusNM = 3440.065

// Point is a (latitude, longitude) pair in decimal degrees.
type Point struct {
	Lat, Lon float64
}

// YSSY34L is the runway 34L departure threshold (touchdown end for 16R
// landings, liftoff end for 34L departures). Public airport data.
var YSSY34L = Point{Lat: -33.9624, Lon: 151.1803}

// NOTE: the observer (home) location is intentionally NOT a source-code
// constant. It's injected at runtime via env (HOME_LAT/HOME_LON), set from
// a sensitive Terraform variable, so this repo can be published without
// leaking the operator's address.

// DistanceNM returns the great-circle distance between a and b in nautical miles.
func DistanceNM(a, b Point) float64 {
	lat1 := rad(a.Lat)
	lat2 := rad(b.Lat)
	dLat := rad(b.Lat - a.Lat)
	dLon := rad(b.Lon - a.Lon)

	s := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1)*math.Cos(lat2)*math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(s), math.Sqrt(1-s))
	return earthRadiusNM * c
}

// BearingDeg returns the initial bearing from a to b in degrees [0, 360).
func BearingDeg(a, b Point) float64 {
	lat1 := rad(a.Lat)
	lat2 := rad(b.Lat)
	dLon := rad(b.Lon - a.Lon)

	y := math.Sin(dLon) * math.Cos(lat2)
	x := math.Cos(lat1)*math.Sin(lat2) -
		math.Sin(lat1)*math.Cos(lat2)*math.Cos(dLon)
	br := deg(math.Atan2(y, x))
	return math.Mod(br+360, 360)
}

// HeadingWithin reports whether heading h (degrees) lies within ±tol of center,
// handling 360° wrap-around.
func HeadingWithin(h, center, tol float64) bool {
	d := math.Mod(math.Abs(h-center)+540, 360) - 180
	return math.Abs(d) <= tol
}

// CrossTrackNM returns the signed perpendicular distance from observer to the
// great-circle path starting at origin and heading along bearingDeg. Sign is
// positive if observer is to the right of the track, negative to the left.
// Magnitude is in nautical miles.
func CrossTrackNM(origin, observer Point, bearingDeg float64) float64 {
	d13 := DistanceNM(origin, observer) / earthRadiusNM
	b13 := rad(BearingDeg(origin, observer))
	b12 := rad(bearingDeg)
	return math.Asin(math.Sin(d13)*math.Sin(b13-b12)) * earthRadiusNM
}

// AlongTrackNM returns the distance along the great-circle path from origin
// (heading bearingDeg) to the point closest to observer. Negative means the
// closest point is behind origin.
func AlongTrackNM(origin, observer Point, bearingDeg float64) float64 {
	d13 := DistanceNM(origin, observer) / earthRadiusNM
	xt := CrossTrackNM(origin, observer, bearingDeg) / earthRadiusNM
	// Clamp to avoid NaN from floating-point overshoot when d13 ≈ |xt|.
	c := math.Cos(d13) / math.Cos(xt)
	if c > 1 {
		c = 1
	} else if c < -1 {
		c = -1
	}
	at := math.Acos(c) * earthRadiusNM

	// Sign: if observer's bearing from origin is more than 90° off heading,
	// closest approach is behind the origin (negative).
	dBearing := math.Mod(math.Abs(BearingDeg(origin, observer)-bearingDeg)+540, 360) - 180
	if math.Abs(dBearing) > 90 {
		return -at
	}
	return at
}

func rad(d float64) float64 { return d * math.Pi / 180 }
func deg(r float64) float64 { return r * 180 / math.Pi }

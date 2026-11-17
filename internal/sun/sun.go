// Package sun computes where the Sun stood for an observer, and when the
// twilight crossings of one night happen.
//
// The model is the NOAA solar calculator's: Meeus' low-precision solar
// coordinates, good to about a hundredth of a degree for dates around the
// present. That is two orders of magnitude finer than the readings the analysis
// reports, and it needs no ephemeris or network. Altitudes are geometric: no
// atmospheric refraction, which is also how the twilight angles are defined.
package sun

import (
	"fmt"
	"math"
	"time"
)

// Depression angles of the Sun's centre below the horizon, in degrees.
const (
	Civil        = 6.0  // civil twilight
	Nautical     = 12.0 // nautical twilight
	Astronomical = 18.0 // astronomical twilight
)

// Altitude returns the geometric altitude of the Sun's centre in degrees at t
// for an observer at latitude and longitude in degrees (north and east
// positive). Negative values are below the horizon.
func Altitude(t time.Time, latitude, longitude float64) float64 {
	s := solarState(t)
	utc := t.UTC()
	// True solar time in minutes: the clock, corrected by the equation of time
	// and by the observer's distance from the meridian (4 minutes per degree).
	minutes := float64(utc.Hour()*60+utc.Minute()) +
		float64(utc.Second())/60 + float64(utc.Nanosecond())/6e10
	trueSolar := math.Mod(minutes+s.equationOfTime+4*longitude, 1440)
	if trueSolar < 0 {
		trueSolar += 1440
	}
	hourAngle := trueSolar/4 - 180

	lat, dec := radians(latitude), radians(s.declination)
	sinAlt := math.Sin(lat)*math.Sin(dec) +
		math.Cos(lat)*math.Cos(dec)*math.Cos(radians(hourAngle))
	return degrees(math.Asin(clamp(sinAlt, -1, 1)))
}

// BelowHorizon returns how many degrees the Sun's centre sat below the horizon
// at t, positive at night. It is the report's statistic: 18 means astronomical
// night, 0 the Sun on the horizon, negative daylight.
func BelowHorizon(t time.Time, latitude, longitude float64) float64 {
	return -Altitude(t, latitude, longitude)
}

// Visibility says how the Sun met the twilight angle on the day asked for.
type Visibility int

const (
	// Crosses is a normal day: the Sun rises above the angle and falls below it
	// again, so Dawn and Dusk exist.
	Crosses Visibility = iota
	// StaysUp means the Sun never fell below the angle: no twilight, and at the
	// astronomical angle no night at all (a summer night at high latitude).
	StaysUp
	// StaysDown means the Sun never rose above the angle: darkness all day, with
	// no dawn or dusk to name.
	StaysDown
)

// Night is one night's crossings of a twilight angle.
type Night struct {
	// Dawn and Dusk are UTC. They are zero unless Visibility is Crosses.
	Dawn, Dusk time.Time
	Visibility Visibility
}

// Twilight returns the dawn and dusk at which the Sun crosses depression degrees
// below the horizon, for the night centred on day's solar noon at the site.
//
// "Centred on the solar noon" is what makes the pair belong to one night: for a
// site west of Greenwich, dawn is on the UTC date before dusk, and for one east
// of it both can be on the same UTC date. day's calendar date at UTC is the
// anchor, not a local date the caller would have to compute.
func Twilight(day time.Time, latitude, longitude, depression float64) (Night, error) {
	if latitude < -90 || latitude > 90 {
		return Night{}, fmt.Errorf("latitude %g is outside [-90, 90]", latitude)
	}
	if longitude < -180 || longitude > 180 {
		return Night{}, fmt.Errorf("longitude %g is outside [-180, 180]", longitude)
	}
	if depression <= 0 || depression >= 90 {
		return Night{}, fmt.Errorf("depression angle %g is outside (0, 90)", depression)
	}

	utc := day.UTC()
	base := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)

	// The solar transit (local noon) is close to 12:00 UTC minus four minutes per
	// degree of longitude, moved by the equation of time. Refining once at the
	// estimate is enough: the state changes by seconds across the shift.
	transit := 720 - 4*longitude - solarState(base).equationOfTime
	at := base.Add(minutesToDuration(transit))
	state := solarState(at)
	transit = 720 - 4*longitude - state.equationOfTime

	lat, dec := radians(latitude), radians(state.declination)
	cosH := (math.Sin(radians(-depression)) - math.Sin(lat)*math.Sin(dec)) /
		(math.Cos(lat) * math.Cos(dec))
	switch {
	case cosH < -1:
		return Night{Visibility: StaysUp}, nil
	case cosH > 1:
		return Night{Visibility: StaysDown}, nil
	}

	// The hour-angle estimate places the crossing within minutes, but it uses the
	// Sun's declination at transit, and over the hours between noon and twilight
	// that moves enough to miss the angle by a tenth of a degree. Bisect the
	// altitude itself instead: the crossing is within twelve hours of transit and
	// the altitude is monotonic over that span.
	noon := base.Add(minutesToDuration(transit))
	return Night{
		Dawn:       crossing(noon, -1, latitude, longitude, depression),
		Dusk:       crossing(noon, +1, latitude, longitude, depression),
		Visibility: Crosses,
	}, nil
}

// crossing finds the instant the Sun crosses depression degrees below the
// horizon, direction -1 towards dawn and +1 towards dusk. It bisects between
// solar noon, where the Sun is highest, and twelve hours later, where it is
// lowest: a crossing the hour-angle test admitted always lies inside that span.
func crossing(noon time.Time, direction int, latitude, longitude, depression float64) time.Time {
	span := 12 * time.Hour * time.Duration(direction)
	lo, hi := noon, noon.Add(span)
	for i := 0; i < 48; i++ {
		mid := lo.Add(hi.Sub(lo) / 2)
		if BelowHorizon(mid, latitude, longitude) < depression {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo.Add(hi.Sub(lo) / 2)
}

// AstronomicalTwilight is Twilight at the angle that ends astronomical night.
func AstronomicalTwilight(day time.Time, latitude, longitude float64) (Night, error) {
	return Twilight(day, latitude, longitude, Astronomical)
}

// state is the Sun's position for one instant, in the two forms the altitude and
// transit maths need.
type state struct {
	declination    float64 // degrees
	equationOfTime float64 // minutes
}

// solarState evaluates the NOAA solar position equations at t.
func solarState(t time.Time) state {
	jc := julianCentury(t)

	// Geometric mean longitude and mean anomaly of the Sun.
	l0 := mod360(280.46646 + jc*(36000.76983+jc*0.0003032))
	m := 357.52911 + jc*(35999.05029-0.0001537*jc)
	ecc := 0.016708634 - jc*(0.000042037+0.0000001267*jc)

	// Equation of the centre, then the true and apparent longitudes.
	centre := math.Sin(radians(m))*(1.914602-jc*(0.004817+0.000014*jc)) +
		math.Sin(radians(2*m))*(0.019993-0.000101*jc) +
		math.Sin(radians(3*m))*0.000289
	omega := 125.04 - 1934.136*jc
	apparent := l0 + centre - 0.00569 - 0.00478*math.Sin(radians(omega))

	// Obliquity of the ecliptic, with the nutation correction that matches the
	// apparent longitude.
	meanObliquity := 23 + (26+(21.448-jc*(46.815+jc*(0.00059-jc*0.001813)))/60)/60
	obliquity := meanObliquity + 0.00256*math.Cos(radians(omega))

	declination := degrees(math.Asin(
		math.Sin(radians(obliquity)) * math.Sin(radians(apparent))))

	// Equation of time, in minutes.
	y := math.Pow(math.Tan(radians(obliquity/2)), 2)
	eqTime := 4 * degrees(
		y*math.Sin(2*radians(l0))-
			2*ecc*math.Sin(radians(m))+
			4*ecc*y*math.Sin(radians(m))*math.Cos(2*radians(l0))-
			0.5*y*y*math.Sin(4*radians(l0))-
			1.25*ecc*ecc*math.Sin(2*radians(m)))

	return state{declination: declination, equationOfTime: eqTime}
}

// julianCentury is the number of Julian centuries since J2000.0.
func julianCentury(t time.Time) float64 {
	return (julianDay(t) - 2451545.0) / 36525.0
}

// julianDay is the Julian date, with the Unix epoch at JD 2440587.5.
func julianDay(t time.Time) float64 {
	utc := t.UTC()
	return 2440587.5 + float64(utc.Unix())/86400 + float64(utc.Nanosecond())/86400e9
}

// minutesToDuration converts a possibly fractional, possibly negative number of
// minutes into a Duration. time.Duration is a float64 after the multiply, so the
// conversion truncates at the nanosecond, far below the model's accuracy.
func minutesToDuration(minutes float64) time.Duration {
	return time.Duration(minutes * float64(time.Minute))
}

func mod360(v float64) float64 {
	v = math.Mod(v, 360)
	if v < 0 {
		v += 360
	}
	return v
}

func radians(deg float64) float64 { return deg * math.Pi / 180 }
func degrees(rad float64) float64 { return rad * 180 / math.Pi }

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

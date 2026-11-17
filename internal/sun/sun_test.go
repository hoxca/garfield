package sun

import (
	"math"
	"testing"
	"time"
)

func TestAltitudeAtTheEquatorPassesNearTheZenith(t *testing.T) {
	// At the March equinox the Sun's declination is near zero, so at the equator
	// and near local noon it is close to straight overhead. 12:00 UTC is a few
	// minutes from the meridian because of the equation of time.
	got := Altitude(time.Date(2024, 3, 20, 12, 0, 0, 0, time.UTC), 0, 0)
	if got < 88 || got > 90 {
		t.Errorf("altitude = %.3f, want the Sun near the zenith", got)
	}
	if below := BelowHorizon(time.Date(2024, 3, 20, 12, 0, 0, 0, time.UTC), 0, 0); below != -got {
		t.Errorf("BelowHorizon = %v, want -Altitude = %v", below, -got)
	}
}

// The strongest self-check: the times the twilight search returns must put the
// Sun on the angle that was searched for. A sign error or a wrong hour angle
// fails here even if the times look plausible.
func TestTwilightCrossingsReachTheAngle(t *testing.T) {
	const lat, lon = 51.4779, -0.0015 // Greenwich
	for _, tc := range []struct {
		name       string
		day        time.Time
		depression float64
	}{
		{"astronomical in late summer", time.Date(2024, 8, 21, 0, 0, 0, 0, time.UTC), Astronomical},
		{"astronomical at midwinter", time.Date(2024, 12, 21, 0, 0, 0, 0, time.UTC), Astronomical},
		{"civil at equinox", time.Date(2024, 3, 20, 0, 0, 0, 0, time.UTC), Civil},
		{"nautical at equinox", time.Date(2024, 3, 20, 0, 0, 0, 0, time.UTC), Nautical},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, err := Twilight(tc.day, lat, lon, tc.depression)
			if err != nil {
				t.Fatalf("Twilight: %v", err)
			}
			if n.Visibility != Crosses {
				t.Fatalf("visibility = %v, want Crosses at this latitude", n.Visibility)
			}
			if !n.Dawn.Before(n.Dusk) {
				t.Errorf("dawn %v is not before dusk %v", n.Dawn, n.Dusk)
			}
			for _, got := range []struct {
				when string
				at   time.Time
			}{{"dawn", n.Dawn}, {"dusk", n.Dusk}} {
				below := BelowHorizon(got.at, lat, lon)
				if math.Abs(below-tc.depression) > 0.01 {
					t.Errorf("%s puts the Sun %.3f degrees below the horizon, want %.1f", got.when, below, tc.depression)
				}
			}
		})
	}
}

func TestSunriseAtGreenwichMatchesTheAlmanac(t *testing.T) {
	// 2024-06-21 at Greenwich: sunrise 03:43 UTC, sunset 20:21 UTC. The 0.833°
	// depression is the standard one: refraction plus the solar semi-diameter.
	n, err := Twilight(time.Date(2024, 6, 21, 0, 0, 0, 0, time.UTC), 51.4779, -0.0015, 0.833)
	if err != nil {
		t.Fatalf("Twilight: %v", err)
	}
	if n.Visibility != Crosses {
		t.Fatalf("visibility = %v, want Crosses", n.Visibility)
	}
	for _, tc := range []struct {
		name string
		got  time.Time
		want time.Time
	}{
		{"sunrise", n.Dawn, time.Date(2024, 6, 21, 3, 43, 0, 0, time.UTC)},
		{"sunset", n.Dusk, time.Date(2024, 6, 21, 20, 21, 0, 0, time.UTC)},
	} {
		if d := tc.got.Sub(tc.want); d > 3*time.Minute || d < -3*time.Minute {
			t.Errorf("%s = %v, want %v (±3m)", tc.name, tc.got.Format("15:04"), tc.want.Format("15:04"))
		}
	}
}

// London is far enough north to lose astronomical night around midsummer: the
// Sun never reaches eighteen degrees below the horizon, so there is nothing to
// cross and no dawn or dusk to name.
func TestHighLatitudeSummerHasNoAstronomicalNight(t *testing.T) {
	n, err := AstronomicalTwilight(time.Date(2024, 6, 21, 0, 0, 0, 0, time.UTC), 51.4779, -0.0015)
	if err != nil {
		t.Fatalf("AstronomicalTwilight: %v", err)
	}
	if n.Visibility != StaysUp {
		t.Errorf("visibility = %v, want StaysUp at London midsummer", n.Visibility)
	}
}

func TestPolarDayAndNight(t *testing.T) {
	const lat, lon = 85.0, 0.0
	day, err := AstronomicalTwilight(time.Date(2024, 6, 21, 0, 0, 0, 0, time.UTC), lat, lon)
	if err != nil {
		t.Fatalf("midsummer: %v", err)
	}
	if day.Visibility != StaysUp {
		t.Errorf("midsummer visibility = %v, want StaysUp (no astronomical night)", day.Visibility)
	}
	if !day.Dawn.IsZero() || !day.Dusk.IsZero() {
		t.Errorf("midsummer returned crossings: dawn %v, dusk %v", day.Dawn, day.Dusk)
	}

	night, err := AstronomicalTwilight(time.Date(2024, 12, 21, 0, 0, 0, 0, time.UTC), lat, lon)
	if err != nil {
		t.Fatalf("midwinter: %v", err)
	}
	if night.Visibility != StaysDown {
		t.Errorf("midwinter visibility = %v, want StaysDown (dark all day)", night.Visibility)
	}
}

func TestTwilightRejectsImpossibleInputs(t *testing.T) {
	day := time.Date(2024, 3, 20, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		lat, lon   float64
		depression float64
	}{
		{"latitude too far north", 91, 0, Astronomical},
		{"latitude too far south", -91, 0, Astronomical},
		{"longitude too far east", 0, 181, Astronomical},
		{"longitude too far west", 0, -181, Astronomical},
		{"zero depression", 0, 0, 0},
		{"negative depression", 0, 0, -6},
		{"zenith depression", 0, 0, 90},
	} {
		if _, err := Twilight(day, tc.lat, tc.lon, tc.depression); err == nil {
			t.Errorf("%s: Twilight accepted the input", tc.name)
		}
	}
}

// The date anchor is the UTC calendar date of the argument, so the same night
// asked for by any time on that date gives the same crossings.
func TestTwilightIsAnchoredOnTheUTCDate(t *testing.T) {
	morning := time.Date(2024, 12, 21, 1, 0, 0, 0, time.UTC)
	evening := time.Date(2024, 12, 21, 23, 0, 0, 0, time.UTC)
	a, err := AstronomicalTwilight(morning, 51.4779, -0.0015)
	if err != nil {
		t.Fatalf("morning: %v", err)
	}
	b, err := AstronomicalTwilight(evening, 51.4779, -0.0015)
	if err != nil {
		t.Fatalf("evening: %v", err)
	}
	if !a.Dawn.Equal(b.Dawn) || !a.Dusk.Equal(b.Dusk) {
		t.Errorf("crossings differ across the date: %v..%v vs %v..%v", a.Dawn, a.Dusk, b.Dawn, b.Dusk)
	}
}

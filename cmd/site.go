package cmd

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"garfield/internal/readfits"
	"garfield/internal/sun"
)

// The Sun's depth below the horizon is a property of the header, not of the
// pixels: SITELAT and SITELONG place the observer, and DATE-OBS (UTC) places the
// instant. The acquisition writes the site as a sexagesimal string -- "+44 20
// 14" -- but the cards may also be plain numbers, so both are accepted.

// sunBelowDecimals is the precision the statistic is kept at. A hundredth of a
// degree is finer than the model's own accuracy and is what the report shows, so
// rounding here keeps the value in the cache identical to the one in the CSV.
const sunBelowDecimals = 2

// sunBelowAt returns how many degrees the Sun sat below the horizon when the
// frame was taken. Zero means the header did not record a usable site and time;
// the report leaves the column empty rather than claiming the Sun was on the
// horizon.
func sunBelowAt(path string) float64 {
	cards, err := readfits.ReadHeaderValues(path, "SITELAT", "SITELONG", "DATE-OBS")
	if err != nil {
		return 0
	}
	below, ok := sunBelowFromCards(cards)
	if !ok {
		return 0
	}
	return below
}

// sunBelowFromCards computes the depth from a frame's header cards. ok is false
// when one of the three is missing or cannot be parsed.
func sunBelowFromCards(cards map[string]string) (below float64, ok bool) {
	lat, err := parseSiteDegrees(cards["SITELAT"], 90)
	if err != nil {
		return 0, false
	}
	lon, err := parseSiteDegrees(cards["SITELONG"], 180)
	if err != nil {
		return 0, false
	}
	at, err := parseObservationTime(cards["DATE-OBS"])
	if err != nil {
		return 0, false
	}
	return roundTo(sun.BelowHorizon(at, lat, lon), sunBelowDecimals), true
}

// parseSiteDegrees reads a latitude or longitude card.
//
// Accepted forms are decimal degrees ("44.3372", "-0.5"), sexagesimal with
// spaces ("+44 20 14", "-00 30 00") and with colons ("+01:01:30"). The sign
// belongs to the whole value, so "-00 30 00" is half a degree south, not half a
// degree minus nothing. limit is 90 for a latitude and 180 for a longitude.
func parseSiteDegrees(value string, limit float64) (float64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("not recorded")
	}

	sign := 1.0
	switch value[0] {
	case '+':
		value = value[1:]
	case '-':
		sign = -1
		value = value[1:]
	}

	parts := strings.Fields(value)
	if len(parts) == 1 && strings.Contains(parts[0], ":") {
		parts = strings.Split(parts[0], ":")
	}
	if len(parts) > 3 {
		return 0, fmt.Errorf("%q has more than three sexagesimal parts", value)
	}

	var total float64
	for i, part := range parts {
		part = strings.Trim(part, `°'"`)
		n, err := strconv.ParseFloat(part, 64)
		if err != nil {
			return 0, fmt.Errorf("%q is not a number", part)
		}
		switch i {
		case 0:
			total = n
		case 1:
			if n < 0 || n >= 60 {
				return 0, fmt.Errorf("minutes %v are outside [0, 60)", n)
			}
			total += n / 60
		case 2:
			if n < 0 || n >= 60 {
				return 0, fmt.Errorf("seconds %v are outside [0, 60)", n)
			}
			total += n / 3600
		}
	}

	total *= sign
	if total < -limit || total > limit {
		return 0, fmt.Errorf("%v is outside [-%v, %v]", total, limit, limit)
	}
	return total, nil
}

// parseObservationTime reads DATE-OBS. FITS defines it as UTC, but writers differ
// on whether the separator is a T and whether a zone is appended, so the common
// spellings are tried and a zone-less value is taken as UTC.
func parseObservationTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("not recorded")
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04",
	} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not an ISO 8601 time", value)
}

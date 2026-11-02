package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"garfield/internal/readfits"
)

// cameraAuto selects camera detection from the FITS header. It is the default,
// so a run that does not name a camera reads the first light frame's INSTRUME
// instead of assuming the instrument.
const cameraAuto = "auto"

// cameraHint is appended to every auto-detection failure. Detection is
// mandatory in auto mode, so the only way forward is to name the camera.
const cameraHint = "pass --camera c3 or --camera g4 to choose one explicitly"

// Instrument identifiers as the cameras write them into the primary header.
// Matching is case-insensitive and by substring, so a firmware suffix or a
// different spelling of the model does not defeat detection.
const (
	c3Instrument      = "C3-61000"
	g4Instrument      = "GX/CX"
	asi2600Instrument = "ASI2600"
)

// knownCameras lists every --camera value that names an acquisition camera.
// It includes instruments whose filename structure is not implemented yet: an
// exclusion from cameraProfiles means "recognised but not preparable", which is
// a different message from a typo. Adding the profile later needs no change
// here.
var knownCameras = map[string]bool{
	"c3":      true,
	"g4":      true,
	"asi2600": true,
}

// detectCameraName maps an INSTRUME value to the camera it names. ok is false
// for a value that names no camera this tool knows.
func detectCameraName(instrume string) (name string, ok bool) {
	v := strings.ToUpper(strings.TrimSpace(instrume))
	switch {
	case strings.Contains(v, c3Instrument):
		return "c3", true
	case strings.Contains(v, g4Instrument):
		return "g4", true
	case strings.Contains(v, asi2600Instrument):
		return "asi2600", true
	default:
		return "", false
	}
}

// cameraNameFromLights reads INSTRUME from the target's first light frame and
// returns the camera it names.
//
// The frame is the first FITS file of the chronologically first YYYY-MM-DD
// session, in sorted filter and filename order. Any frame in that session would
// do -- a target has one camera -- so this only needs to reach a reliable one.
// Frames whose header cannot be read, or which carry no INSTRUME, are skipped
// in favour of the next; a frame that names an unrecognised instrument stops
// the search, because continuing would hide a real inconsistency.
func cameraNameFromLights(lightsRoot string) (string, error) {
	entries, err := os.ReadDir(lightsRoot)
	if err != nil {
		return "", fmt.Errorf("unable to read %q: %w", lightsRoot, err)
	}

	var dates []string
	for _, e := range entries {
		if e.IsDir() && sessionDateRe.MatchString(e.Name()) {
			dates = append(dates, e.Name())
		}
	}
	sort.Strings(dates)
	if len(dates) == 0 {
		return "", fmt.Errorf(
			"cannot detect the camera: no YYYY-MM-DD session under %q; %s",
			lightsRoot, cameraHint)
	}

	var attempts []string
	for _, date := range dates {
		sessionDir := filepath.Join(lightsRoot, date)
		filters, _, err := filterSubdirs(sessionDir)
		if err != nil {
			return "", err
		}
		for _, filter := range filters {
			files, err := listFITS(filepath.Join(sessionDir, filter))
			if err != nil {
				return "", err
			}
			for _, name := range files {
				path := filepath.Join(sessionDir, filter, name)
				instrume, found, err := readfits.ReadHeaderString(path, "INSTRUME")
				switch {
				case err != nil:
					attempts = append(attempts, fmt.Sprintf("%s: %v", path, err))
					continue
				case !found || instrume == "":
					attempts = append(attempts, fmt.Sprintf("%s: no INSTRUME card", path))
					continue
				}
				if camera, ok := detectCameraName(instrume); ok {
					return camera, nil
				}
				return "", fmt.Errorf(
					"cannot detect the camera: INSTRUME %q in %q does not name a known camera; %s",
					instrume, path, cameraHint)
			}
		}
	}

	detail := ""
	if len(attempts) > 0 {
		detail = "; tried " + strings.Join(attempts, "; ")
	}
	return "", fmt.Errorf(
		"cannot detect the camera: no light frame with a usable INSTRUME under %q%s; %s",
		lightsRoot, detail, cameraHint)
}

// resolveCamera turns the --camera value into a profile. "auto" (and the empty
// value, which a zero prepareOptions carries) reads the header; an explicit
// name is resolved directly, without touching the FITS files. A recognised
// camera without a profile fails here rather than planning with another
// camera's rules.
func resolveCamera(name, lightsRoot string) (cameraProfile, error) {
	if name == "" || name == cameraAuto {
		detected, err := cameraNameFromLights(lightsRoot)
		if err != nil {
			return cameraProfile{}, err
		}
		return cameraProfileFor(detected)
	}
	return cameraProfileFor(name)
}

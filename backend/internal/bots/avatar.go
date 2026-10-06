package bots

import (
	"errors"
	"fmt"
	"regexp"
)

// ErrInvalidAvatarSeed indicates that a bot avatar seed failed validation.
var ErrInvalidAvatarSeed = errors.New("avatar seed must be 1..100 characters matching ^[a-z0-9-]+$")

var avatarSeedPattern = regexp.MustCompile(`^[a-z0-9-]+$`)

// AvatarPalette defines the SVG colour tokens selected for a robot avatar.
type AvatarPalette struct {
	Name   string `json:"name"`
	Body   string `json:"body"`
	Shade  string `json:"shade"`
	Panel  string `json:"panel"`
	Sensor string `json:"sensor"`
	Mark   string `json:"mark"`
	Accent string `json:"accent"`
	Tile   string `json:"tile"`
}

// AvatarGeometry holds part-dependent SVG layout coordinates matching Avatar.dc.html.
type AvatarGeometry struct {
	Top    int `json:"top"`
	TipY   int `json:"tipY"`
	EarL   int `json:"earL"`
	EarR   int `json:"earR"`
	PanelX int `json:"panelX"`
	PanelW int `json:"panelW"`
}

// AvatarSpec contains the deterministic hash, selected parts, palette, and SVG geometry.
type AvatarSpec struct {
	Seed         string         `json:"seed"`
	Hash         uint32         `json:"hash"`
	HeadIndex    int            `json:"headIndex"`
	Head         string         `json:"head"`
	EyesIndex    int            `json:"eyesIndex"`
	Eyes         string         `json:"eyes"`
	AntennaIndex int            `json:"antennaIndex"`
	Antenna      string         `json:"antenna"`
	MouthIndex   int            `json:"mouthIndex"`
	Mouth        string         `json:"mouth"`
	PaletteIndex int            `json:"paletteIndex"`
	Palette      AvatarPalette  `json:"palette"`
	Geometry     AvatarGeometry `json:"geometry"`
}

var (
	avatarHeads   = []string{"rect", "hex", "dome", "oct"}
	avatarEyes    = []string{"dual", "visor", "cyclops", "led"}
	avatarAnts    = []string{"ball", "double", "gear"}
	avatarMouths  = []string{"grille", "dots", "slot"}
	avatarTops    = []int{38, 36, 30, 38}
	avatarPalette = []AvatarPalette{
		{Name: "forest", Body: "#3f9147", Shade: "#347a3b", Panel: "#182019", Sensor: "#d6fcd0", Mark: "#182019", Accent: "#3f9147", Tile: "#eef8ec"},
		{Name: "night", Body: "#2a3c2a", Shade: "#1f2d1f", Panel: "#14201a", Sensor: "#5eab60", Mark: "#5eab60", Accent: "#3f9147", Tile: "#d6fcd0"},
		{Name: "steel", Body: "#c9d8c9", Shade: "#a8bda8", Panel: "#1f2d1f", Sensor: "#5eab60", Mark: "#1f2d1f", Accent: "#3f9147", Tile: "#f5f8f4"},
		{Name: "cobalt", Body: "#4a5e92", Shade: "#3f5f9c", Panel: "#14201a", Sensor: "#8aa8db", Mark: "#14201a", Accent: "#4a5e92", Tile: "#e4e9f4"},
		{Name: "sky", Body: "#8aa8db", Shade: "#6d8fce", Panel: "#1f2d1f", Sensor: "#d6fcd0", Mark: "#1f2d1f", Accent: "#4a5e92", Tile: "#edf0f6"},
		{Name: "mint", Body: "#d6fcd0", Shade: "#b5e4ae", Panel: "#2a3c2a", Sensor: "#49b24d", Mark: "#2a3c2a", Accent: "#3f9147", Tile: "#e3ebe2"},
	}
)

// ValidateAvatarSeed verifies that seed obeys the schema regex ^[a-z0-9-]+$
// and fits within the VARCHAR(100) database limit.
func ValidateAvatarSeed(seed string) error {
	if len(seed) == 0 || len(seed) > 100 || !avatarSeedPattern.MatchString(seed) {
		return ErrInvalidAvatarSeed
	}
	return nil
}

// FNV1a32 computes the unsigned 32-bit FNV-1a hash matching design/STYLEGUIDE.md §8
// and design/source/Avatar.dc.html. Seeds are ASCII ([a-z0-9-]), so byte-wise
// iteration matches UTF-16 charCodeAt.
func FNV1a32(seed string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(seed); i++ {
		h ^= uint32(seed[i])
		h *= 16777619
	}
	return h
}

// ComputeAvatarSpec derives the deterministic robot avatar parts, palette, and
// SVG geometry for a validated seed.
func ComputeAvatarSpec(seed string) (AvatarSpec, error) {
	if err := ValidateAvatarSeed(seed); err != nil {
		return AvatarSpec{}, fmt.Errorf("validate avatar seed: %w", err)
	}

	h := FNV1a32(seed)
	// Safe conversions: modulo results are bounded in [0, 5].
	headIdx := int(h % 4)          // #nosec G115
	eyesIdx := int((h >> 4) % 4)   // #nosec G115
	antIdx := int((h >> 8) % 3)    // #nosec G115
	mouthIdx := int((h >> 12) % 3) // #nosec G115
	palIdx := int((h >> 16) % 6)   // #nosec G115

	top := avatarTops[headIdx]
	tipY := top - 15
	switch antIdx {
	case 0:
		tipY = top - 16
	case 1:
		tipY = top - 13
	}

	earL, earR, panelX, panelW := 18, 93, 36, 48
	if headIdx == 1 {
		earL, earR, panelX, panelW = 16, 95, 38, 44
	}

	return AvatarSpec{
		Seed:         seed,
		Hash:         h,
		HeadIndex:    headIdx,
		Head:         avatarHeads[headIdx],
		EyesIndex:    eyesIdx,
		Eyes:         avatarEyes[eyesIdx],
		AntennaIndex: antIdx,
		Antenna:      avatarAnts[antIdx],
		MouthIndex:   mouthIdx,
		Mouth:        avatarMouths[mouthIdx],
		PaletteIndex: palIdx,
		Palette:      avatarPalette[palIdx],
		Geometry: AvatarGeometry{
			Top:    top,
			TipY:   tipY,
			EarL:   earL,
			EarR:   earR,
			PanelX: panelX,
			PanelW: panelW,
		},
	}, nil
}

package light

import (
	_ "embed"
	"encoding/json"
)

//go:embed presets-v1.json
var presetsData []byte

// Preset 是内置灯效预设（presets-v1.json 的元素）。
type Preset struct {
	PresetID    string           `json:"presetId"`
	DisplayName string           `json:"displayName"`
	Description string           `json:"description"`
	RepeatTimes int              `json:"repeat_times"`
	Segments    []map[string]any `json:"segments"`
	Base64      string           `json:"base64"`
}

type presetsFile struct {
	Version string   `json:"version"`
	Presets []Preset `json:"presets"`
}

var loadedPresets []Preset

func init() {
	var f presetsFile
	if err := json.Unmarshal(presetsData, &f); err == nil {
		loadedPresets = f.Presets
	}
}

// Presets 返回全部内置预设。
func Presets() []Preset {
	return loadedPresets
}

// LookupPreset 按 presetId 查找内置预设。
func LookupPreset(id string) (Preset, bool) {
	for _, p := range loadedPresets {
		if p.PresetID == id {
			return p, true
		}
	}
	return Preset{}, false
}

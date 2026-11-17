package main

type BoardStatus struct {
Platform         string `json:"platform"`
Connected        bool   `json:"connected"`
HandleHex        string `json:"handle_hex"`
StateHex         string `json:"state_hex"`
FirmwareVer      string `json:"firmware_ver"`
SerialNo         string `json:"serial_no"`
ChipIDState      string `json:"chip_id_state"`
ListReady        bool   `json:"list_ready"`
ListStateHex     string `json:"list_state_hex"`
GalvoX           uint16 `json:"galvo_x"`
GalvoY           uint16 `json:"galvo_y"`
InputPortHex     string `json:"input_port_hex"`
InputPortBin     string `json:"input_port_bin"`
OutputPortHex    string `json:"output_port_hex"`
OutputPortBin    string `json:"output_port_bin"`
FieldSizeMM      string `json:"field_size_mm"`
TotalParts       string `json:"total_parts"`
TotalTimeSec     string `json:"total_time_sec"`
HeadUnlocked     bool   `json:"head_unlocked"`
ButtonPressed    bool   `json:"button_pressed"`
PreviewActive    bool   `json:"preview_active"`
PreviewRemainSec int    `json:"preview_remain_sec"`
ErrorMsg         string `json:"error_msg,omitempty"`
}

type MarkRequest struct {
Shape     string  `json:"shape"`      // "circle", "square", "cross"
SizeMM    float64 `json:"size_mm"`    // 目標直徑或邊長 (mm)
ScaleX    float64 `json:"scale_x"`    // X 軸真圓度比例補償 (預設 1.000)
ScaleY    float64 `json:"scale_y"`    // Y 軸真圓度比例補償 (預設 1.000)
PowerPct  float64 `json:"power_pct"`  // 雷射功率百分比 (1 ~ 100%)
SpeedWord uint16  `json:"speed_word"` // 雕刻速度參數 (預設 348)
FreqWord  uint16  `json:"freq_word"`  // 光纖頻率參數 (預設 1000)
}

type LaserController interface {
GetStatus() BoardStatus
WriteOutputPort(mask uint16, val uint16) error
GotoXY(x uint16, y uint16) error
RedLightPreview(sizeMM float64, speedParam int) error
StartContinuousPreview(sizeMM float64, speedParam int, timeoutSec int) error
StopPreview() error
ExecuteLaserMark(req MarkRequest) error
}
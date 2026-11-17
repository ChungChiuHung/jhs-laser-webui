package main

// BoardStatus 定義回傳給 Web UI 的完整設備狀態
type BoardStatus struct {
Platform     string `json:"platform"`
Connected    bool   `json:"connected"`
HandleHex    string `json:"handle_hex"`
StateHex     string `json:"state_hex"`
FirmwareVer  string `json:"firmware_ver"`
SerialNo     string `json:"serial_no"`
ChipIDState  string `json:"chip_id_state"`
ListReady    bool   `json:"list_ready"`
ListStateHex string `json:"list_state_hex"`
GalvoX       uint16 `json:"galvo_x"`
GalvoY       uint16 `json:"galvo_y"`
InputPortHex string `json:"input_port_hex"`
InputPortBin string `json:"input_port_bin"`
LaserReady   bool   `json:"laser_ready"` // In0
XOrg0        bool   `json:"x_org0"`      // In14 (CON4 Pin 1)
YOrg0        bool   `json:"y_org0"`      // In15 (CON4 Pin 2)
ErrorMsg     string `json:"error_msg,omitempty"`
}

// LaserController 抽象介面，隔離 Windows DLL 與 Linux 實作
type LaserController interface {
GetStatus() BoardStatus
WriteOutputPort(mask uint16, val uint16) error
GotoXY(x uint16, y uint16) error
}
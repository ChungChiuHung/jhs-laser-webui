package main

// BoardStatus 定義回傳給 Web UI 的完整設備狀態
type BoardStatus struct {
.\.git\Platform      string `json:"platform"`
.\.git\Connected     bool   `json:"connected"`
.\.git\HandleHex     string `json:"handle_hex"`
.\.git\StateHex      string `json:"state_hex"`
.\.git\FirmwareVer   string `json:"firmware_ver"`
.\.git\SerialNo      string `json:"serial_no"`
.\.git\ChipIDState   string `json:"chip_id_state"`
.\.git\ListReady     bool   `json:"list_ready"`
.\.git\ListStateHex  string `json:"list_state_hex"`
.\.git\GalvoX        uint16 `json:"galvo_x"`
.\.git\GalvoY        uint16 `json:"galvo_y"`
.\.git\InputPortHex  string `json:"input_port_hex"`
.\.git\InputPortBin  string `json:"input_port_bin"`
.\.git\OutputPortHex string `json:"output_port_hex"`
.\.git\OutputPortBin string `json:"output_port_bin"`
.\.git\LaserReady    bool   `json:"laser_ready"` // In0
.\.git\XOrg0         bool   `json:"x_org0"`      // In14 (CON4 Pin 1)
.\.git\YOrg0         bool   `json:"y_org0"`      // In15 (CON4 Pin 2)
.\.git\ErrorMsg      string `json:"error_msg,omitempty"`
}

// LaserController 抽象介面，隔離 Windows DLL 與 Linux 實作
type LaserController interface {
.\.git\GetStatus() BoardStatus
.\.git\WriteOutputPort(mask uint16, val uint16) error
.\.git\GotoXY(x uint16, y uint16) error
}
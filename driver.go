package main

type BoardStatus struct {
Platform      string `json:"platform"`
Connected     bool   `json:"connected"`
HandleHex     string `json:"handle_hex"`
StateHex      string `json:"state_hex"`
FirmwareVer   string `json:"firmware_ver"`
SerialNo      string `json:"serial_no"`
ChipIDState   string `json:"chip_id_state"`
ListReady     bool   `json:"list_ready"`
ListStateHex  string `json:"list_state_hex"`
GalvoX        uint16 `json:"galvo_x"`
GalvoY        uint16 `json:"galvo_y"`
InputPortHex  string `json:"input_port_hex"`
InputPortBin  string `json:"input_port_bin"`
OutputPortHex string `json:"output_port_hex"`
OutputPortBin string `json:"output_port_bin"`
LaserReady    bool   `json:"laser_ready"`
XOrg0         bool   `json:"x_org0"`
YOrg0         bool   `json:"y_org0"`
ErrorMsg      string `json:"error_msg,omitempty"`
}

type LaserController interface {
GetStatus() BoardStatus
WriteOutputPort(mask uint16, val uint16) error
GotoXY(x uint16, y uint16) error
}
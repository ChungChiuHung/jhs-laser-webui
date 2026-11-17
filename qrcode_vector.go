package main

import (
"fmt"
"math"
"sort"
"strings"

"rsc.io/qr"
)

type QRVectorRequest struct {
Text        string  `json:"text"`
SizeMM      float64 `json:"size_mm"`      // 目標尺寸 (mm)，預設 20
SizeBasis   string  `json:"size_basis"`   // "total" (含靜音區), "code" (僅碼區), "module" (單點寬)
QuietZone   int     `json:"quiet_zone"`   // 靜音區模組數，預設 4
ECC         string  `json:"ecc"`          // "L", "M", "Q", "H"
HatchStepMM float64 `json:"hatch_step_mm"`// 雷射填充線間距 (mm)，預設 0.08 (0 = 僅外輪廓)
ScaleX      float64 `json:"scale_x"`      // X 軸比例微調
ScaleY      float64 `json:"scale_y"`      // Y 軸比例微調
PowerPct    float64 `json:"power_pct"`    // 打標功率 (%)
SpeedWord   uint16  `json:"speed_word"`   // 打標速度
}

type QRVectorResponse struct {
OK           bool          `json:"ok"`
Error        string        `json:"error,omitempty"`
ModuleCount  int           `json:"module_count"`
QuietZone    int           `json:"quiet_zone"`
ECC          string        `json:"ecc"`
OverallMM    float64       `json:"overall_mm"`
CodeMM       float64       `json:"code_mm"`
ModuleMM     float64       `json:"module_mm"`
ContourCount int           `json:"contour_count"`
ContoursMM   [][][2]float64 `json:"contours_mm"`
SVG          string        `json:"svg"`
DXF          string        `json:"dxf"`
}

func fmtFloat(n float64) string {
v := math.Round(n*1000.0) / 1000.0
s := fmt.Sprintf("%.3f", v)
s = strings.TrimRight(s, "0")
s = strings.TrimRight(s, ".")
if s == "" || s == "-0" {
return "0"
}
return s
}

func parseECC(ecc string) (qr.Level, string) {
switch strings.ToUpper(strings.TrimSpace(ecc)) {
case "L":
return qr.L, "L"
case "Q":
return qr.Q, "Q"
case "H":
return qr.H, "H"
default:
return qr.M, "M"
}
}

// traceQRContours 100% 移植附件 HTML 中的邊界追蹤演算法：合併相鄰暗模組，消除內部重複切割線
func traceQRContours(code *qr.Code) [][][2]int {
n := code.Size
isDark := func(r, c int) bool {
return r >= 0 && c >= 0 && r < n && c < n && code.Black(c, r)
}

type dirVec [2]int
out := make(map[string][]dirVec)
totalEdges := 0

addEdge := func(x, y, dx, dy int) {
k := fmt.Sprintf("%d,%d", x, y)
out[k] = append(out[k], dirVec{dx, dy})
totalEdges++
}

for r := 0; r < n; r++ {
for c := 0; c < n; c++ {
if !isDark(r, c) {
continue
}
if !isDark(r-1, c) {
addEdge(c, r, 1, 0)
}
if !isDark(r, c+1) {
addEdge(c+1, r, 0, 1)
}
if !isDark(r+1, c) {
addEdge(c+1, r+1, -1, 0)
}
if !isDark(r, c-1) {
addEdge(c, r+1, 0, -1)
}
}
}

takeEdge := func(x, y, pdx, pdy int) (dirVec, bool) {
k := fmt.Sprintf("%d,%d", x, y)
list := out[k]
if len(list) == 0 {
return dirVec{}, false
}
prefs := []dirVec{{-pdy, pdx}, {pdx, pdy}, {pdy, -pdx}}
for _, pref := range prefs {
for j, cand := range list {
if cand == pref {
out[k] = append(list[:j], list[j+1:]...)
return cand, true
}
}
}
chosen := list[0]
out[k] = list[1:]
return chosen, true
}

keys := make([]string, 0, len(out))
for k := range out {
keys = append(keys, k)
}
sort.Slice(keys, func(i, j int) bool {
var x1, y1, x2, y2 int
fmt.Sscanf(keys[i], "%d,%d", &x1, &y1)
fmt.Sscanf(keys[j], "%d,%d", &x2, &y2)
if y1 != y2 {
return y1 < y2
}
return x1 < x2
})

var loops [][][2]int
for _, k := range keys {
for len(out[k]) > 0 {
var sx, sy int
fmt.Sscanf(k, "%d,%d", &sx, &sy)
dir := out[k][0]
out[k] = out[k][1:]

pts := [][2]int{{sx, sy}}
x, y := sx+dir[0], sy+dir[1]
guard := 0
for !(x == sx && y == sy) {
nd, ok := takeEdge(x, y, dir[0], dir[1])
guard++
if !ok || guard > totalEdges+8 {
break
}
if nd[0] != dir[0] || nd[1] != dir[1] {
pts = append(pts, [2]int{x, y})
}
dir = nd
x += dir[0]
y += dir[1]
}
loops = append(loops, splitSimplePolygon(pts)...)
}
}
return loops
}

// splitSimplePolygon 拆分對角接觸的自我交會頂點，確保 CAD / EzCad2 匯入不報錯
func splitSimplePolygon(pts [][2]int) [][][2]int {
var result [][][2]int
var stack [][2]int
pos := make(map[string]int)

for _, pt := range pts {
k := fmt.Sprintf("%d,%d", pt[0], pt[1])
if at, exists := pos[k]; exists {
sub := make([][2]int, len(stack[at:]))
copy(sub, stack[at:])
for j := at + 1; j < len(stack); j++ {
delete(pos, fmt.Sprintf("%d,%d", stack[j][0], stack[j][1]))
}
stack = stack[:at+1]
result = append(result, sub)
} else {
pos[k] = len(stack)
stack = append(stack, pt)
}
}
if len(stack) >= 3 {
result = append(result, stack)
}
return result
}

func GenerateQRVector(req QRVectorRequest) QRVectorResponse {
text := strings.TrimSpace(req.Text)
if text == "" {
return QRVectorResponse{OK: false, Error: "請輸入要編碼的文字或網址 (text 不可為空)"}
}
if req.SizeMM <= 0 {
req.SizeMM = 20.0
}
if req.SizeMM > 95.0 {
req.SizeMM = 95.0
}
quiet := req.QuietZone
if quiet < 0 {
quiet = 4
}
if quiet > 10 {
quiet = 10
}

lvl, eccLabel := parseECC(req.ECC)
code, err := qr.Encode(text, lvl)
if err != nil {
return QRVectorResponse{OK: false, Error: fmt.Sprintf("QRCode 編碼失敗: %v", err)}
}

n := code.Size
totalModules := n + quiet*2
var moduleMM float64
switch strings.ToLower(strings.TrimSpace(req.SizeBasis)) {
case "module":
moduleMM = req.SizeMM
case "code":
moduleMM = req.SizeMM / float64(n)
default:
moduleMM = req.SizeMM / float64(totalModules)
}

overallMM := moduleMM * float64(totalModules)
codeMM := moduleMM * float64(n)
loops := traceQRContours(code)

// 1. 產生雷射專用合併輪廓 SVG (1 unit = 1 mm)
var svgPaths []string
var contoursMM [][][2]float64
for _, pts := range loops {
var seg strings.Builder
var polyMM [][2]float64
for j, pt := range pts {
px := float64(pt[0]+quiet) * moduleMM
py := float64(pt[1]+quiet) * moduleMM
if j == 0 {
seg.WriteString("M" + fmtFloat(px) + "," + fmtFloat(py) + " ")
} else {
seg.WriteString("L" + fmtFloat(px) + "," + fmtFloat(py) + " ")
}
polyMM = append(polyMM, [2]float64{px, py})
}
seg.WriteString("Z")
svgPaths = append(svgPaths, seg.String())
contoursMM = append(contoursMM, polyMM)
}

var svgBuf strings.Builder
svgBuf.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
svgBuf.WriteString(fmt.Sprintf("<svg xmlns=\"http://www.w3.org/2000/svg\" version=\"1.1\" width=\"%smm\" height=\"%smm\" viewBox=\"0 0 %s %s\">\n",
fmtFloat(overallMM), fmtFloat(overallMM), fmtFloat(overallMM), fmtFloat(overallMM)))
svgBuf.WriteString("<path fill=\"#000000\" fill-rule=\"evenodd\" stroke=\"none\" d=\"")
svgBuf.WriteString(strings.Join(svgPaths, "\n"))
svgBuf.WriteString("\"/>\n</svg>\n")

// 2. 產生 AutoCAD R12 ASCII DXF (Y 軸翻轉為 CAD Y-up 座標，可直接匯入 EzCad2 / LightBurn)
dxfLines := []string{
"0", "SECTION", "2", "HEADER",
"9", "$ACADVER", "1", "AC1009",
"9", "$EXTMIN", "10", "0", "20", "0", "30", "0",
"9", "$EXTMAX", "10", fmtFloat(overallMM), "20", fmtFloat(overallMM), "30", "0",
"0", "ENDSEC",
"0", "SECTION", "2", "ENTITIES",
}
for _, pts := range loops {
dxfLines = append(dxfLines, "0", "POLYLINE", "8", "QR", "66", "1", "10", "0", "20", "0", "30", "0", "70", "1")
for _, pt := range pts {
x := float64(pt[0]+quiet) * moduleMM
y := overallMM - float64(pt[1]+quiet)*moduleMM
dxfLines = append(dxfLines, "0", "VERTEX", "8", "QR", "10", fmtFloat(x), "20", fmtFloat(y), "30", "0")
}
dxfLines = append(dxfLines, "0", "SEQEND", "8", "QR")
}
dxfLines = append(dxfLines, "0", "ENDSEC", "0", "EOF")

return QRVectorResponse{
OK:           true,
ModuleCount:  n,
QuietZone:    quiet,
ECC:          eccLabel,
OverallMM:    math.Round(overallMM*1000) / 1000,
CodeMM:       math.Round(codeMM*1000) / 1000,
ModuleMM:     math.Round(moduleMM*1000) / 1000,
ContourCount: len(loops),
ContoursMM:   contoursMM,
SVG:          svgBuf.String(),
DXF:          strings.Join(dxfLines, "\r\n") + "\r\n",
}
}
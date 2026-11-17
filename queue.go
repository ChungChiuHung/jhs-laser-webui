package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ===== 資料結構 =====

type QueueItem struct {
	ID           string  `json:"id"`
	LotID        int     `json:"lot_id"`
	LotName      string  `json:"lot_name"`
	ProductCode  string  `json:"product_code"`
	ProductName  string  `json:"product_name"`
	CustomerName string  `json:"customer_name"`
	Material     string  `json:"material"`
	Surface      string  `json:"surface"`
	Token        string  `json:"token"`
	TraceURL     string  `json:"trace_url"`
	Status       string  `json:"status"` // pending | active | done | failed
	SizeMM       float64 `json:"size_mm"`
	PowerPct     float64 `json:"power_pct"`
	SpeedWord    uint16  `json:"speed_word"`
	HatchStepMM  float64 `json:"hatch_step_mm"`
	CreatedAt    string  `json:"created_at"`
	MarkedAt     string  `json:"marked_at,omitempty"`
	LastError    string  `json:"last_error,omitempty"`
}

const (
	stPending = "pending"
	stActive  = "active"
	stDone    = "done"
	stFailed  = "failed"

	previewTimeoutSec = 120
)

func validStatus(s string) bool {
	return s == stPending || s == stActive || s == stDone || s == stFailed
}

// ===== 材質參數預設集 =====

type laserPreset struct {
	PowerPct    float64
	SpeedWord   uint16
	HatchStepMM float64
}

func materialPreset(material string) laserPreset {
	m := strings.ToUpper(strings.TrimSpace(material))
	switch {
	case strings.Contains(m, "SUS"), strings.Contains(m, "不鏽鋼"), strings.Contains(m, "不锈钢"), strings.Contains(m, "STAINLESS"):
		return laserPreset{80, 800, 0.03}
	case strings.HasPrefix(m, "AL"), strings.Contains(m, "ALUM"), strings.HasPrefix(m, "A6"), strings.HasPrefix(m, "A7"), strings.Contains(m, "鋁"), strings.Contains(m, "铝"):
		return laserPreset{60, 1000, 0.04}
	case strings.Contains(m, "銅"), strings.Contains(m, "铜"), strings.HasPrefix(m, "CU"), strings.Contains(m, "BRASS"):
		return laserPreset{90, 600, 0.03}
	default:
		return laserPreset{70, 900, 0.04}
	}
}

// ===== 佇列儲存 (JSON 持久化 + RWMutex) =====

type QueueStore struct {
	mu    sync.RWMutex
	path  string
	items []*QueueItem
}

func NewQueueStore(dataDir string) (*QueueStore, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	s := &QueueStore{path: filepath.Join(dataDir, "queue.json")}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return s, nil
	}
	var items []*QueueItem
	if err := json.Unmarshal(b, &items); err != nil {
		return nil, fmt.Errorf("queue.json 解析失敗: %w", err)
	}
	s.items = items
	// 重啟後不應殘留 active 的「執行中」假象，但保留其為選中項目
	return s, nil
}

// saveLocked 需在持有寫鎖時呼叫；先寫暫存檔再 rename，避免寫到一半斷電造成檔案損毀。
func (s *QueueStore) saveLocked() error {
	b, err := json.MarshalIndent(s.items, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func itemKey(it *QueueItem) string {
	if it.Token != "" {
		return "t:" + it.Token
	}
	return "l:" + it.LotName + "|" + it.TraceURL
}

func (s *QueueStore) findLocked(id string) *QueueItem {
	for _, it := range s.items {
		if it.ID == id {
			return it
		}
	}
	return nil
}

func (s *QueueStore) activeLocked() *QueueItem {
	for _, it := range s.items {
		if it.Status == stActive {
			return it
		}
	}
	return nil
}

func (s *QueueStore) firstPendingLocked() *QueueItem {
	for _, it := range s.items {
		if it.Status == stPending {
			return it
		}
	}
	return nil
}

// Enqueue 具冪等性：以 Token（或 LotName+TraceURL）為唯一鍵。
func (s *QueueStore) Enqueue(in []QueueItem) (enqueued int, totalPending int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Format(time.RFC3339)
	index := map[string]*QueueItem{}
	for _, it := range s.items {
		index[itemKey(it)] = it
	}
	for i := range in {
		n := in[i]
		n.LotName = strings.TrimSpace(n.LotName)
		if n.LotName == "" && n.Token == "" {
			continue
		}
		if ex, ok := index[itemKey(&n)]; ok {
			// 更新描述性欄位；有明確指定的雷射參數才覆蓋
			ex.LotID, ex.LotName = n.LotID, n.LotName
			ex.ProductCode, ex.ProductName = n.ProductCode, n.ProductName
			ex.CustomerName, ex.Material, ex.Surface = n.CustomerName, n.Material, n.Surface
			if n.TraceURL != "" {
				ex.TraceURL = n.TraceURL
			}
			if n.SizeMM > 0 {
				ex.SizeMM = n.SizeMM
			}
			if n.PowerPct > 0 {
				ex.PowerPct = n.PowerPct
			}
			if n.SpeedWord > 0 {
				ex.SpeedWord = n.SpeedWord
			}
			if n.HatchStepMM > 0 {
				ex.HatchStepMM = n.HatchStepMM
			}
			if ex.Status == stDone || ex.Status == stFailed {
				// 重刻：重置為 pending 並移到佇列尾端
				ex.Status = stPending
				ex.MarkedAt, ex.LastError = "", ""
				s.removeLocked(ex.ID)
				s.items = append(s.items, ex)
			}
			enqueued++
			continue
		}
		n.ID = n.Token
		if n.ID == "" {
			n.ID = newID()
		}
		n.Status = stPending
		n.CreatedAt = now
		n.MarkedAt, n.LastError = "", ""
		p := materialPreset(n.Material)
		if n.SizeMM <= 0 {
			n.SizeMM = 12.0
		}
		if n.PowerPct <= 0 {
			n.PowerPct = p.PowerPct
		}
		if n.SpeedWord == 0 {
			n.SpeedWord = p.SpeedWord
		}
		if n.HatchStepMM <= 0 {
			n.HatchStepMM = p.HatchStepMM
		}
		cp := n
		s.items = append(s.items, &cp)
		index[itemKey(&cp)] = &cp
		enqueued++
	}
	if err = s.saveLocked(); err != nil {
		return 0, 0, err
	}
	for _, it := range s.items {
		if it.Status == stPending || it.Status == stActive {
			totalPending++
		}
	}
	return enqueued, totalPending, nil
}

func (s *QueueStore) removeLocked(id string) {
	for i, it := range s.items {
		if it.ID == id {
			s.items = append(s.items[:i], s.items[i+1:]...)
			return
		}
	}
}

type queueSnapshot struct {
	Items    []QueueItem
	Pending  int
	Active   int
	Done     int
	Failed   int
	Selected *QueueItem
}

func (s *QueueStore) Snapshot() queueSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap := queueSnapshot{Items: make([]QueueItem, 0, len(s.items))}
	for _, it := range s.items {
		snap.Items = append(snap.Items, *it)
		switch it.Status {
		case stPending:
			snap.Pending++
		case stActive:
			snap.Active++
			c := *it
			snap.Selected = &c
		case stDone:
			snap.Done++
		case stFailed:
			snap.Failed++
		}
	}
	return snap
}

func (s *QueueStore) Get(id string) (QueueItem, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if it := s.findLocked(id); it != nil {
		return *it, true
	}
	return QueueItem{}, false
}

var errNotFound = fmt.Errorf("找不到指定的佇列項目")
var errIsDone = fmt.Errorf("此項目已完成；請先將狀態改回 pending 才能重新打標")

// Select 將指定項目設為 active，原 active 退回 pending。
func (s *QueueStore) Select(id string) (QueueItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.findLocked(id)
	if it == nil {
		return QueueItem{}, errNotFound
	}
	if it.Status == stDone {
		return QueueItem{}, errIsDone
	}
	s.activateLocked(it)
	if err := s.saveLocked(); err != nil {
		return QueueItem{}, err
	}
	return *it, nil
}

func (s *QueueStore) activateLocked(it *QueueItem) {
	for _, o := range s.items {
		if o != it && o.Status == stActive {
			o.Status = stPending
		}
	}
	it.Status = stActive
	it.LastError = ""
}

// UpdateParams 保存前端調整後的雷射參數。
func (s *QueueStore) UpdateParams(id string, size, power float64, speed uint16, hatch float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.findLocked(id)
	if it == nil {
		return
	}
	if size > 0 {
		it.SizeMM = size
	}
	if power > 0 {
		it.PowerPct = power
	}
	if speed > 0 {
		it.SpeedWord = speed
	}
	if hatch > 0 {
		it.HatchStepMM = hatch
	}
	_ = s.saveLocked()
}

// FinishMark 記錄打標結果；成功且 autoNext 時啟用下一筆 pending。
func (s *QueueStore) FinishMark(id string, markErr error, autoNext bool) (done QueueItem, next *QueueItem, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.findLocked(id)
	if it == nil {
		return QueueItem{}, nil, errNotFound
	}
	if markErr != nil {
		it.Status = stFailed
		it.LastError = markErr.Error()
		saveErr := s.saveLocked()
		return *it, nil, saveErr
	}
	it.Status = stDone
	it.MarkedAt = time.Now().Format(time.RFC3339)
	it.LastError = ""
	if autoNext {
		if n := s.firstPendingLocked(); n != nil {
			s.activateLocked(n)
			c := *n
			next = &c
		}
	}
	return *it, next, s.saveLocked()
}

func (s *QueueStore) SetStatus(id, status string) (QueueItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.findLocked(id)
	if it == nil {
		return QueueItem{}, errNotFound
	}
	if status == stActive {
		s.activateLocked(it)
	} else {
		it.Status = status
	}
	if status == stPending {
		it.MarkedAt, it.LastError = "", ""
	}
	return *it, s.saveLocked()
}

func (s *QueueStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findLocked(id) == nil {
		return errNotFound
	}
	s.removeLocked(id)
	return s.saveLocked()
}

func (s *QueueStore) ClearDone() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.items[:0:0]
	removed := 0
	for _, it := range s.items {
		if it.Status == stDone {
			removed++
			continue
		}
		kept = append(kept, it)
	}
	s.items = kept
	return removed, s.saveLocked()
}

// ===== HTTP =====

type queueAPI struct {
	store  *QueueStore
	ctl    LaserController
	markMu sync.Mutex // 同一時間僅允許一筆實體打標
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]any{"ok": false, "error": err.Error()})
}

func errCode(err error) int {
	switch err {
	case errNotFound:
		return http.StatusNotFound
	case errIsDone:
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

// withCORS 為 queue API 加入 CORS 與 POST/OPTIONS 處理。
func withCORS(method string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != method {
			writeErr(w, http.StatusMethodNotAllowed, fmt.Errorf("Method Not Allowed"))
			return
		}
		h(w, r)
	}
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("JSON 格式錯誤: %v", err))
		return false
	}
	return true
}

func qrReqFor(it QueueItem) QRVectorRequest {
	text := it.TraceURL
	if text == "" {
		text = it.Token
	}
	if text == "" {
		text = it.LotName
	}
	return QRVectorRequest{
		Text: text, SizeMM: it.SizeMM, HatchStepMM: it.HatchStepMM,
		PowerPct: it.PowerPct, SpeedWord: it.SpeedWord,
	}
}

func (a *queueAPI) hardware() map[string]any {
	st := a.ctl.GetStatus()
	return map[string]any{
		"connected":      st.Connected,
		"preview_active": st.PreviewActive,
		"head_unlocked":  st.HeadUnlocked,
		"simulation":     strings.Contains(st.Platform, "Simulation"),
		"platform":       st.Platform,
	}
}

func registerQueueRoutes(mux *http.ServeMux, ctl LaserController, store *QueueStore, webFS fs.FS) *queueAPI {
	a := &queueAPI{store: store, ctl: ctl}

	mux.HandleFunc("/queue", func(w http.ResponseWriter, r *http.Request) {
		b, err := fs.ReadFile(webFS, "queue.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
	})

	mux.HandleFunc("/api/queue", withCORS(http.MethodGet, func(w http.ResponseWriter, r *http.Request) {
		s := store.Snapshot()
		writeJSON(w, 200, map[string]any{
			"ok": true, "items": s.Items,
			"pending_count": s.Pending, "active_count": s.Active,
			"done_count": s.Done, "failed_count": s.Failed,
			"selected": s.Selected, "hardware": a.hardware(),
		})
	}))

	mux.HandleFunc("/api/queue/enqueue", withCORS(http.MethodPost, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Items []QueueItem `json:"items"`
		}
		if !decodeBody(w, r, &req) {
			return
		}
		n, pending, err := store.Enqueue(req.Items)
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "enqueued": n, "total_pending": pending})
	}))

	mux.HandleFunc("/api/queue/select", withCORS(http.MethodPost, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"id"`
		}
		if !decodeBody(w, r, &req) {
			return
		}
		it, err := store.Select(req.ID)
		if err != nil {
			writeErr(w, errCode(err), err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "item": it, "qr": GenerateQRVector(qrReqFor(it))})
	}))

	mux.HandleFunc("/api/queue/preview", withCORS(http.MethodPost, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     string  `json:"id"`
			Action string  `json:"action"`
			SizeMM float64 `json:"size_mm"`
		}
		if !decodeBody(w, r, &req) {
			return
		}
		it, ok := store.Get(req.ID)
		if !ok {
			writeErr(w, 404, errNotFound)
			return
		}
		size := req.SizeMM
		if size <= 0 {
			size = it.SizeMM
		}
		active := a.ctl.GetStatus().PreviewActive
		action := req.Action
		if action == "toggle" || action == "" {
			if active {
				action = "stop"
			} else {
				action = "start"
			}
		}
		switch action {
		case "start":
			store.UpdateParams(it.ID, size, 0, 0, 0)
			// 紅光框以實際打標外框 (OverallMM) 為準
			qr := GenerateQRVector(QRVectorRequest{Text: qrReqFor(it).Text, SizeMM: size})
			frame := size
			if qr.OK && qr.OverallMM > 0 {
				frame = qr.OverallMM
			}
			if err := a.ctl.StartShapePreview("square", frame, 0, previewTimeoutSec); err != nil {
				writeErr(w, 500, err)
				return
			}
		case "stop":
			_ = a.ctl.StopPreview()
		default:
			writeErr(w, 400, fmt.Errorf("action 必須為 start / stop / toggle"))
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "action": action, "size_mm": size,
			"preview_active": a.ctl.GetStatus().PreviewActive})
	}))

	mux.HandleFunc("/api/queue/mark", withCORS(http.MethodPost, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID          string  `json:"id"`
			SizeMM      float64 `json:"size_mm"`
			PowerPct    float64 `json:"power_pct"`
			SpeedWord   uint16  `json:"speed_word"`
			HatchStepMM float64 `json:"hatch_step_mm"`
			AutoNext    bool    `json:"auto_next"`
		}
		if !decodeBody(w, r, &req) {
			return
		}
		it, ok := store.Get(req.ID)
		if !ok {
			writeErr(w, 404, errNotFound)
			return
		}
		if it.Status == stDone {
			writeErr(w, 409, errIsDone)
			return
		}
		if !a.markMu.TryLock() {
			writeErr(w, 409, fmt.Errorf("雷射正在打標另一筆，請稍候"))
			return
		}
		defer a.markMu.Unlock()

		store.UpdateParams(it.ID, req.SizeMM, req.PowerPct, req.SpeedWord, req.HatchStepMM)
		if _, err := store.Select(it.ID); err != nil {
			writeErr(w, errCode(err), err)
			return
		}
		it, _ = store.Get(it.ID)

		if a.ctl.GetStatus().PreviewActive {
			_ = a.ctl.StopPreview()
		}
		res, markErr := a.ctl.ExecuteQRCodeMark(qrReqFor(it))
		if markErr == nil && !res.OK {
			markErr = fmt.Errorf("%s", res.Error)
		}
		done, next, err := store.FinishMark(it.ID, markErr, req.AutoNext)
		if markErr != nil {
			writeJSON(w, 500, map[string]any{"ok": false, "error": markErr.Error(), "item": done})
			return
		}
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		out := map[string]any{"ok": true, "item": done, "overall_mm": res.OverallMM, "next": nil}
		if next != nil {
			out["next"] = map[string]any{"item": *next, "qr": GenerateQRVector(qrReqFor(*next))}
		}
		writeJSON(w, 200, out)
	}))

	// 手動改狀態 / 刪除單筆：{"id","status"} 或 {"id","action":"delete"}
	mux.HandleFunc("/api/queue/status", withCORS(http.MethodPost, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Action string `json:"action"`
		}
		if !decodeBody(w, r, &req) {
			return
		}
		if req.Action == "delete" {
			if err := store.Delete(req.ID); err != nil {
				writeErr(w, errCode(err), err)
				return
			}
			writeJSON(w, 200, map[string]any{"ok": true, "deleted": req.ID})
			return
		}
		if !validStatus(req.Status) {
			writeErr(w, 400, fmt.Errorf("status 必須為 pending / active / done / failed"))
			return
		}
		it, err := store.SetStatus(req.ID, req.Status)
		if err != nil {
			writeErr(w, errCode(err), err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "item": it})
	}))

	mux.HandleFunc("/api/queue/clear-done", withCORS(http.MethodPost, func(w http.ResponseWriter, r *http.Request) {
		n, err := store.ClearDone()
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "removed": n})
	}))

	return a
}

//go:build !windows

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewQueueStore(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerQueueRoutes(mux, NewLaserController(""), store, fstest.MapFS{"queue.html": {Data: []byte("<html>q</html>")}})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, dir
}

func post(t *testing.T, srv *httptest.Server, path string, body any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(srv.URL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	out["_code"] = float64(resp.StatusCode)
	return out
}

func getQueue(t *testing.T, srv *httptest.Server) map[string]any {
	resp, err := http.Get(srv.URL + "/api/queue")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func sample(tok, lot, mat string) map[string]any {
	return map[string]any{"lot_name": lot, "token": tok, "material": mat, "product_name": "P",
		"trace_url": "https://trace.johesheng.com/p/" + tok}
}

func TestEnqueueIdempotentAndPresets(t *testing.T) {
	srv, dir := newTestServer(t)
	r := post(t, srv, "/api/queue/enqueue", map[string]any{"items": []any{sample("a", "LOT-1", "SUS304"), sample("b", "LOT-2", "鋁合金")}})
	if r["enqueued"].(float64) != 2 || r["total_pending"].(float64) != 2 {
		t.Fatalf("bad enqueue: %v", r)
	}
	// 重複送入同 token：更新而非新增
	upd := sample("a", "LOT-1", "SUS316")
	upd["product_name"] = "NEW"
	r = post(t, srv, "/api/queue/enqueue", map[string]any{"items": []any{upd}})
	if r["total_pending"].(float64) != 2 {
		t.Fatalf("dup created card: %v", r)
	}
	q := getQueue(t, srv)
	items := q["items"].([]any)
	first := items[0].(map[string]any)
	if first["product_name"] != "NEW" || first["power_pct"].(float64) != 80 || first["speed_word"].(float64) != 800 || first["hatch_step_mm"].(float64) != 0.03 || first["size_mm"].(float64) != 12 {
		t.Fatalf("preset/update wrong: %v", first)
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "queue.json")); err != nil {
		t.Fatal("queue.json missing")
	}
}

func TestFlowSelectPreviewMarkAutoNextAndRework(t *testing.T) {
	srv, dir := newTestServer(t)
	post(t, srv, "/api/queue/enqueue", map[string]any{"items": []any{sample("a", "LOT-1", "SUS304"), sample("b", "LOT-2", "SUS304")}})

	s := post(t, srv, "/api/queue/select", map[string]any{"id": "a"})
	if s["ok"] != true || s["qr"].(map[string]any)["ok"] != true {
		t.Fatalf("select: %v", s)
	}
	p := post(t, srv, "/api/queue/preview", map[string]any{"id": "a", "action": "toggle", "size_mm": 12})
	if p["preview_active"] != true {
		t.Fatalf("preview not active: %v", p)
	}
	m := post(t, srv, "/api/queue/mark", map[string]any{"id": "a", "size_mm": 12, "power_pct": 80, "speed_word": 800, "hatch_step_mm": 0.03, "auto_next": true})
	if m["ok"] != true || m["item"].(map[string]any)["status"] != "done" {
		t.Fatalf("mark: %v", m)
	}
	nx := m["next"].(map[string]any)["item"].(map[string]any)
	if nx["id"] != "b" || nx["status"] != "active" {
		t.Fatalf("next: %v", nx)
	}
	q := getQueue(t, srv)
	if q["done_count"].(float64) != 1 || q["active_count"].(float64) != 1 || q["hardware"].(map[string]any)["preview_active"] != false {
		t.Fatalf("counts/preview not stopped: %v", q)
	}
	// 完成項目不可直接 select / mark
	if post(t, srv, "/api/queue/select", map[string]any{"id": "a"})["_code"].(float64) != 409 {
		t.Fatal("select done should 409")
	}
	// Odoo 重送 done 項目 -> 重置 pending 並移至尾端
	post(t, srv, "/api/queue/enqueue", map[string]any{"items": []any{sample("a", "LOT-1", "SUS304")}})
	items := getQueue(t, srv)["items"].([]any)
	last := items[len(items)-1].(map[string]any)
	if last["id"] != "a" || last["status"] != "pending" || last["marked_at"] != nil {
		t.Fatalf("rework: %v", last)
	}
	// 持久化：重新載入
	st, err := NewQueueStore(filepath.Join(dir, "data"))
	if err != nil || len(st.Snapshot().Items) != 2 {
		t.Fatalf("reload: %v", err)
	}
}

func TestStatusDeleteClearDoneAndCORS(t *testing.T) {
	srv, _ := newTestServer(t)
	post(t, srv, "/api/queue/enqueue", map[string]any{"items": []any{sample("a", "L1", ""), sample("b", "L2", "")}})
	post(t, srv, "/api/queue/status", map[string]any{"id": "a", "status": "done"})
	if post(t, srv, "/api/queue/status", map[string]any{"id": "a", "status": "bogus"})["_code"].(float64) != 400 {
		t.Fatal("bogus status should 400")
	}
	if r := post(t, srv, "/api/queue/clear-done", map[string]any{}); r["removed"].(float64) != 1 {
		t.Fatalf("clear: %v", r)
	}
	post(t, srv, "/api/queue/status", map[string]any{"id": "b", "action": "delete"})
	if getQueue(t, srv)["pending_count"].(float64) != 0 {
		t.Fatal("delete failed")
	}
	req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/api/queue/enqueue", nil)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 204 || resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("cors: %d", resp.StatusCode)
	}
}

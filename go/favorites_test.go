package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestFavoritesCRUDAndMerge(t *testing.T) {
	tmp := t.TempDir()
	prev := favoritesPathOverride
	favoritesPathOverride = filepath.Join(tmp, "favorites.json")
	defer func() { favoritesPathOverride = prev }()

	callFav := func(method string, body any) (int, struct {
		Ok    bool      `json:"ok"`
		Items []favItem `json:"items"`
		Added bool      `json:"added"`
		Error string    `json:"error"`
	}) {
		var buf bytes.Buffer
		if body != nil {
			if err := json.NewEncoder(&buf).Encode(body); err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(method, "/api/favorites", &buf)
		rec := httptest.NewRecorder()
		handleFavorites(rec, req)
		var out struct {
			Ok    bool      `json:"ok"`
			Items []favItem `json:"items"`
			Added bool      `json:"added"`
			Error string    `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode body=%s err=%v", rec.Body.String(), err)
		}
		return rec.Code, out
	}

	code, res := callFav("GET", nil)
	if code != 200 || !res.Ok || len(res.Items) != 0 {
		t.Fatalf("initial GET code=%d res=%+v", code, res)
	}

	// Toggle adds course
	code, res = callFav("POST", map[string]any{
		"op": "toggle",
		"item": map[string]any{
			"plat": "y2", "id": "202601", "title": "物理化学", "teacher": "刘佳佳", "term": "2026-2027 第一学期",
		},
	})
	if code != 200 || !res.Ok || !res.Added || len(res.Items) != 1 || res.Items[0].Key != "y2:202601" {
		t.Fatalf("toggle add failed: code=%d res=%+v", code, res)
	}

	// Toggle second course puts newest first
	code, res = callFav("POST", map[string]any{
		"op":   "toggle",
		"item": map[string]any{"plat": "old", "id": "74188", "title": "数值分析", "teacher": "孙新"},
	})
	if code != 200 || !res.Added || len(res.Items) != 2 || res.Items[0].Key != "old:74188" {
		t.Fatalf("toggle second failed: code=%d res=%+v", code, res)
	}

	// Merge ignores duplicates and preserves existing items
	code, res = callFav("POST", map[string]any{
		"op": "merge",
		"items": []map[string]any{
			{"plat": "old", "id": "74188", "title": "数值分析"},
			{"plat": "y2", "id": "202699", "title": "离散数学"},
		},
	})
	if code != 200 || len(res.Items) != 3 || res.Items[2].Key != "y2:202699" {
		t.Fatalf("merge failed: code=%d res=%+v", code, res)
	}

	// Toggle existing removes it
	code, res = callFav("POST", map[string]any{
		"op":   "toggle",
		"item": map[string]any{"plat": "old", "id": "74188", "title": "数值分析"},
	})
	if code != 200 || res.Added || len(res.Items) != 2 {
		t.Fatalf("toggle remove failed: code=%d res=%+v", code, res)
	}

	// Remove by key
	code, res = callFav("POST", map[string]any{"op": "remove", "key": "y2:202601"})
	if code != 200 || len(res.Items) != 1 || res.Items[0].Key != "y2:202699" {
		t.Fatalf("remove failed: code=%d res=%+v", code, res)
	}

	// Invalid item returns 400
	code, _ = callFav("POST", map[string]any{"op": "toggle", "item": map[string]any{"plat": "bad", "id": ""}})
	if code != 400 {
		t.Fatalf("expected 400 for invalid item, got %d", code)
	}

	// Clear resets to empty
	code, res = callFav("POST", map[string]any{"op": "clear"})
	if code != 200 || len(res.Items) != 0 {
		t.Fatalf("clear failed: code=%d res=%+v", code, res)
	}
}

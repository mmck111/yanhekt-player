package main

import (
	"testing"
)

// 没有本地索引时（手机第一次用），必须能读到内置课程库 ——
// 否则手机端搜索只能走线上检索，比电脑端慢一大截。
func TestY2CatalogEmbedded(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CACHE_HOME", dir)
	t.Setenv("LOCALAPPDATA", dir)
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)

	cat := y2CatalogLoad()
	if cat == nil || len(cat.Rows) == 0 {
		t.Fatal("读不到内置课程库（web/y2-catalog.json.gz 没打进二进制？）")
	}
	t.Logf("内置课程库 rows=%d at=%d path=%s", len(cat.Rows), cat.At, y2CatPath)
}

package server

import "testing"

func TestTorrentGetShouldLog(t *testing.T) {
	if !torrentGetShouldLog(false, false, 0, 0, 0, 0) {
		t.Fatal("第一次成功查询应该记录")
	}
	if torrentGetShouldLog(false, true, 2, 1, 2, 1) {
		t.Fatal("数量和转译数都没变时不应刷屏")
	}
	if !torrentGetShouldLog(false, true, 2, 1, 3, 1) {
		t.Fatal("数量变化应该记录")
	}
	if !torrentGetShouldLog(false, true, 2, 0, 2, 1) {
		t.Fatal("路径转译数量变化应该记录")
	}
	if !torrentGetShouldLog(true, true, 2, 1, 2, 1) {
		t.Fatal("debug 时即使无变化也记录")
	}
}

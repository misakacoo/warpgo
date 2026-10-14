package main

import "testing"

// 端口冲突的判定是"重装一个已装好的 WireProxy"唯一的拦路石，逐条钉住。
//
// 真机验收逼出来的：第一版只看"端口有没有人在听"，于是在装好的机器上重跑
// `warpgo proxy install`（端口不变）必然失败 —— 而占着那个端口的正是我们自己的
// wireproxy。那会让"重装"退化成"先手动停服务"的两步操作，很不合理。
func TestPortConflict(t *testing.T) {
	cases := []struct {
		name                    string
		busy, installed, active bool
		currentPort, wantPort   int
		want                    bool
	}{
		{"端口空闲 → 不冲突", false, false, false, 0, 40000, false},
		{"端口被占、且我们没装 → 冲突（是别人的）", true, false, false, 0, 40000, true},
		{"端口被占、我们装了但没在跑 → 冲突（可能是残留进程）", true, true, false, 40000, 40000, true},
		{"端口被占、我们正在跑同一个端口 → 不冲突（那是我们自己）", true, true, true, 40000, 40000, false},
		{"端口被占、我们跑在别的端口 → 冲突（新端口是别人的）", true, true, true, 25344, 40000, true},
	}

	for _, tc := range cases {
		got := portConflict(tc.busy, tc.installed, tc.active, tc.currentPort, tc.wantPort)
		if got != tc.want {
			t.Errorf("%s: portConflict(busy=%v installed=%v active=%v current=%d want=%d) = %v，期望 %v",
				tc.name, tc.busy, tc.installed, tc.active, tc.currentPort, tc.wantPort, got, tc.want)
		}
	}
}

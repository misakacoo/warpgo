package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// ErrNoRuntime 表示本机当前没有运行中的隧道。
//
// 与"文件坏了"区分开：前者是正常状态（还没 up 或已经 down），
// 后者需要人来查。上游把两者混在一起，导致"没安装"被当成"读取出错"。
var ErrNoRuntime = errors.New("当前没有运行中的 WARP 隧道")

// Runtime 是运行态：一次 up 成功之后系统处于什么状态。
//
// 它存在的意义是让 `down` 与 `status` 不必去猜 —— 上游靠解析 `ip` 的输出来
// 反推当前状态，于是每次格式变化都要重新适配。这里由我们自己写、自己读。
type Runtime struct {
	IfName string `json:"if_name"`
	Mode   string `json:"mode"`
	// Backend 是**这次实际用上的**后端（kernel / userspace）。
	Backend string `json:"backend"`
	// BackendRequested 是用户**要求的**后端（auto / kernel / userspace）。
	//
	// 两个字段都要，因为它们回答的是不同的问题：日志与 status 想看"现在跑的是什么"，
	// 而保活巡检必须知道你当初是"指定了内核"还是"随便你挑" ——
	// 后者才允许它在内核不通时自动换到用户态。只看实际值会把 auto 悄悄降级成
	// 一个永久的钉死选择。
	BackendRequested string `json:"backend_requested,omitempty"`
	MTU              int    `json:"mtu"`
	Table            int    `json:"table"`
	FWMark           int    `json:"fwmark,omitempty"`

	Families []int    `json:"families"`
	Addrs    []string `json:"addrs"`
	Endpoint string   `json:"endpoint"`

	// Stack 是隧道携带哪几族地址（`both` / `4` / `6`，上游菜单 `4`/`6`/`d`）。
	//
	// 空串按双栈处理（老版本写的运行态文件里没有这个字段）。
	// 它必须持久化：`watch` 重建与 `switch` 互换都要照它来，
	// 否则一次巡检就会把用户选的"只用 IPv4"悄悄改回双栈。
	Stack string `json:"stack,omitempty"`

	// DNS 记录这次 up 时把 /etc/resolv.conf 改成了哪几个服务器（空 = 没改过）。
	//
	// 存在的意义是让"临时关掉再打开"（`off` / `on`）能**原样恢复**：
	// `off` 必须把 resolv.conf 还原回去（否则机器解析不了域名），
	// 而 `on` 要靠这个字段知道当初改成了什么。
	DNS []string `json:"dns,omitempty"`

	// ExitIP / ExitLoc / ExitAt 是**上一次体检看到的公网出口**（空 = 还没体检过）。
	//
	// 为什么要把它持久化，而不是用时现探：菜单顶部那行状态要回答
	// "我现在从哪个 IP 出去"，而那正是打开菜单的人最想知道的一件事；
	// 但菜单是**纯观察**动作（不联网、不做探测、断网时也得能打开），
	// 所以它只能读一个**已经记下来**的值。真实值由落盘的那一方负责写：
	// `up` / `switch` / `on` 重建成功后探一次，保活每轮体检也顺手更新。
	//
	// 注意它**不是**隧道接口地址（172.16.0.2 那种）：那个是隧道内部的私有地址，
	// 与"访问网站时对方看到的 IP"是两件事，混在一起会让人误判自己没走 WARP。
	ExitIP  string    `json:"exit_ip,omitempty"`
	ExitLoc string    `json:"exit_loc,omitempty"`
	ExitAt  time.Time `json:"exit_checked_at,omitempty"`

	UpSince time.Time `json:"up_since"`
}

// Save 写入运行态（0600、原子写）。
func (r *Runtime) Save(path string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("编码运行态: %w", err)
	}
	data = append(data, '\n')
	if err := WriteFileAtomic(path, data, FilePerm); err != nil {
		return fmt.Errorf("保存运行态到 %s: %w", path, err)
	}
	return nil
}

// LoadRuntime 读取运行态；文件不存在时返回 ErrNoRuntime。
func LoadRuntime(path string) (*Runtime, error) {
	raw, found, err := ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrNoRuntime
	}
	var r Runtime
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("解析运行态文件 %s: %w", path, err)
	}
	return &r, nil
}

// RemoveRuntime 删除运行态文件；不存在时不算错（down 要能重复执行）。
func RemoveRuntime(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除运行态文件 %s: %w", path, err)
	}
	return nil
}

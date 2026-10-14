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
	IfName  string `json:"if_name"`
	Mode    string `json:"mode"`
	Backend string `json:"backend"`
	MTU     int    `json:"mtu"`
	Table   int    `json:"table"`
	FWMark  uint32 `json:"fwmark,omitempty"`

	Families []int    `json:"families"`
	Addrs    []string `json:"addrs"`
	Endpoint string   `json:"endpoint"`

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

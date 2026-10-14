// Package rollback 是全局模式的保险丝。
//
// 全局模式和其它操作有一个本质区别：**它有把机器弄失联的能力**。
// 配置一旦写错（端点不可达、规则写反、fwmark 漏掉），整台机器的出网就没了，
// 而它往往是一台只能通过 SSH 摸到的远程服务器 —— 那意味着要重装。
//
// 所以这里的做法不是"小心一点"，而是**预先上保险**：
//
//	快照 → 派生独立的守护进程（setsid，与调用方生命周期无关）
//	     → 应用变更 → 自检 → 通过则 confirm；不通过或超时就无条件回滚
//
// 守护进程独立存在这一点很关键：即使发起变更的那个 SSH 会话已经断了、
// 命令的输出没人看得到，保险丝照样会在若干秒后拉闸。
package rollback

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/misakacoo/warpgo/internal/netcfg"
)

// 运行时文件都放在 /run 下：重启即失效，这正是我们要的语义 ——
// 一个跨越重启还活着的"待回滚"状态只会带来困惑。
const (
	runDirName   = "warpgo"
	snapshotFile = "rollback.json"
	confirmFile  = "rollback.confirm"
	guardLogFile = "guard.log"
	pollInterval = 500 * time.Millisecond
	// DefaultTimeout 是默认的确认窗口。
	//
	// 45 秒是个折中：up 的自检（两次网络探测 + 一次路径检查）最坏约 25 秒，
	// 留出余量；同时它又足够短 —— 真的出事时用户最多等 45 秒就恢复。
	DefaultTimeout = 45 * time.Second
)

// EnvRunDir 可覆盖运行时目录。
//
// 与 `internal/tunnel` 用的是**同一个**环境变量与默认值 —— 它们指的是同一个目录，
// 两处各自硬编码 "/run/warpgo" 迟早会分叉。可覆盖的意义是让真机验收脚本与单测
// 能在一个不碰 /run 的地方把整套流程跑一遍。
const EnvRunDir = "WARPGO_RUN_DIR"

// RunDir 返回运行时目录。
func RunDir() string {
	if v := strings.TrimSpace(os.Getenv(EnvRunDir)); v != "" {
		return v
	}
	return filepath.Join("/run", runDirName)
}

// SnapshotPath / ConfirmPath / GuardLogPath 返回各运行时文件的位置。
func SnapshotPath() string { return filepath.Join(RunDir(), snapshotFile) }
func ConfirmPath() string  { return filepath.Join(RunDir(), confirmFile) }
func GuardLogPath() string { return filepath.Join(RunDir(), guardLogFile) }

// ErrNoSnapshot 表示没有待回滚的快照。
var ErrNoSnapshot = errors.New("没有待回滚的快照")

// Event 是回滚流程里的一个可报告事件。
//
// 库层只**发事件**、不拼文案：文案要跟随用户语言，属于 CLI 层的职责。
// 这样同一套回滚逻辑既能给人类输出，也能在测试里被断言。
type Event int

const (
	// EvArmed 保险丝已就位，开始等待确认（参数：超时、快照路径）。
	EvArmed Event = iota
	// EvConfirmed 收到确认，本次变更保留。
	EvConfirmed
	// EvTimeout 超时未确认，判定变更失败（参数：超时）。
	EvTimeout
	// EvRollbackRules 正在删除策略路由规则。
	EvRollbackRules
	// EvRollbackTable 正在清空路由表（参数：表号）。
	EvRollbackTable
	// EvRollbackLink 正在删除接口（参数：接口名）。
	EvRollbackLink
	// EvRollbackDone 回滚完成。
	EvRollbackDone
	// EvNote 一般性提示（参数：说明文字，通常是给排障看的技术细节）。
	EvNote
	// EvStale 保险丝发现自己守护的那次变更已经不在待确认状态了（快照被换人或被清掉），
	// 于是不动手就退出（参数：说明文字）。
	EvStale
)

// Logger 接收回滚过程中的事件。
type Logger func(Event, ...any)

func nopLogger(Event, ...any) {}

// Snapshot 记录"这次变更动了什么"，回滚时据此清理。
//
// 刻意只记我们自己的东西（接口名、表号、受管的 pref、DNS 指纹），
// 而不是"把整个路由表拍下来再恢复"：后者在实现上更复杂、更容易把系统自带的
// 规则也弄乱，而我们的清理本来就是精确的（按 pref、按表号）。
type Snapshot struct {
	CreatedAt    time.Time `json:"created_at"`
	IfName       string    `json:"if_name"`
	Table        int       `json:"table"`
	Mode         string    `json:"mode"`
	ManagedPrefs []int     `json:"managed_prefs"`
	// ResolvSHA256 是变更前 /etc/resolv.conf 的指纹。
	// 我们不改 DNS，但记下来就能在回滚时判断"是不是有别的东西动过它"。
	ResolvSHA256 string `json:"resolv_sha256,omitempty"`
	PID          int    `json:"pid"`
	Reason       string `json:"reason,omitempty"`
}

// Save 写入快照（0600、原子写）。
func (s *Snapshot) Save(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("编码快照: %w", err)
	}
	data = append(data, '\n')
	if err := writeFileAtomic(path, data); err != nil {
		return err
	}
	return nil
}

// Load 读取快照。
func Load(path string) (*Snapshot, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNoSnapshot
		}
		return nil, fmt.Errorf("读取快照 %s: %w", path, err)
	}
	var s Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("解析快照 %s: %w", path, err)
	}
	return &s, nil
}

// ConfirmFor 写下"**这一次**变更可用"的标记，令对应的守护进程解除保险。
//
// 标记里带的是那一次变更的 CreatedAt —— 这一点很要紧：这块标记是写给
// **某一个**保险丝看的，不是写给"当前所有保险丝"看的。带上身份之后：
//
//   - 旧保险丝不会把新变更的确认误当成自己的（那会让它立刻退出，等于没保险）；
//   - 新保险丝也不会被上一次遗留的标记骗到（那会让它**一开始就不设防**）。
//
// 曾经的实现是"一个不带内容的标记文件 + 每次开始新变更前把它删掉"。
// 那个设计有个必现的漏洞：删标记的人可能在**旧保险丝还没轮到下一次轮询**时
// 就把它删了，于是旧保险丝永远等不到解除，一直守到自己的超时。
// 一旦 up 会因为"自动换后端"而连开两次保险，这个漏洞就会稳定复现。
func ConfirmFor(createdAt time.Time) error {
	if createdAt.IsZero() {
		return errors.New("确认标记必须带上变更的时间戳")
	}
	if err := ensureRunDir(); err != nil {
		return err
	}
	stamp := createdAt.UTC().Format(time.RFC3339Nano) + "\n"
	if err := os.WriteFile(ConfirmPath(), []byte(stamp), 0o600); err != nil {
		return fmt.Errorf("写入确认标记: %w", err)
	}
	return nil
}

// Confirm 确认"当前快照对应的那次变更"。
func Confirm() error {
	snap, err := Load(SnapshotPath())
	if err != nil {
		return err
	}
	return ConfirmFor(snap.CreatedAt)
}

// Disarm 解除保险丝：让等待中的 guard 进程安全退出、不再回滚。
//
// 用在两种场景，它们都**不是**"配置可用"：
//   - 已经主动回滚完了（自检失败那条路径）；
//   - 用户重新发起 up / 执行 down，上一次变更的保险丝不该再插手。
//
// 少了这一步会有一个很隐蔽的故障：一次失败的 up 留下的保险丝在超时后会再回滚一遍，
// 把用户随后（在超时窗口内）重新建立的隧道拆掉 —— 看起来像"随机掉线"。
//
// 没有待确认的变更时它是空操作（也就没有保险丝需要解除）。
func Disarm() error {
	snap, err := Load(SnapshotPath())
	if errors.Is(err, ErrNoSnapshot) {
		return nil
	}
	if err != nil {
		return err
	}
	return ConfirmFor(snap.CreatedAt)
}

// ClearSnapshot 删除快照，表示"当前没有待回滚的变更"。
// up 确认成功之后、以及 down 之后都该调用它。
//
// 注意它**不**顺手删确认标记：标记自带身份（见 ConfirmFor），遗留一份不会影响谁；
// 而删掉它有可能吃掉某个保险丝正等着的那一份，让它白守到超时。
func ClearSnapshot() error {
	if err := os.Remove(SnapshotPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除快照: %w", err)
	}
	return nil
}

// ConfirmedFor 报告"我这次变更"是否已被确认（或被主动解除）。
//
// 只认与自己时间戳相符的标记：认不出来就当作没有 —— 宁可多守一会儿
// （超时那一刻还有 stillPending 兜底），也不能因为一个不相干的标记就撤防。
func ConfirmedFor(createdAt time.Time) bool {
	raw, err := os.ReadFile(ConfirmPath())
	if err != nil {
		return false
	}
	got := strings.TrimSpace(string(raw))
	if got == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, got)
	if err != nil {
		return false
	}
	return t.Equal(createdAt.UTC())
}

// Guard 等待确认；超时未确认就无条件回滚。
//
// 返回 rolled 表示是否真的执行了回滚。
func Guard(timeout time.Duration, snapshotPath string, logf Logger) (bool, error) {
	if logf == nil {
		logf = nopLogger
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	snap, err := Load(snapshotPath)
	if err != nil {
		return false, err
	}

	logf(EvArmed, timeout, snapshotPath)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ConfirmedFor(snap.CreatedAt) {
			// **刻意不删标记**：它是"这一次变更"的，留着不会影响后来者
			// （它们比的是自己那份 CreatedAt）。而在这里删掉反而会误伤 ——
			// 万一发起方已经为**下一次**变更写好了标记，删除就等于把它吃掉，
			// 于是下一次的保险丝也守到超时。
			logf(EvConfirmed)
			return false, nil
		}
		time.Sleep(pollInterval)
	}

	logf(EvTimeout, timeout)

	// 超时了，但先确认"现在还有没有一条等我去回滚的变更"。
	//
	// 保险丝是**异步**的：它出生时守护的那次变更可能早就结束了（成功、或被主动
	// 回滚过），而它自己只是还没轮到下一次轮询。如果这时已经开始了新的变更
	// （快照被换掉）或现场已清干净（快照被删），无脑拉闸就会把**新**隧道拆掉 ——
	// 表现成"随机掉线"，而且极难归因。
	//
	// 单靠"解除标记"（Disarm）是不够的：标记是一个被消费的文件，谁先看到谁删，
	// 两个保险丝共用一个标记时必然有一个会漏看。而快照的 CreatedAt 由发起方写入、
	// 每次变更都不同，用它判断"我还是不是待确认的那一个"是可靠的。
	if !stillPending(snapshotPath, snap) {
		logf(EvStale, "等待期间已经开始了新的变更（快照已被更新或清除），本次保险丝不动手")
		return false, nil
	}

	if err := Rollback(snap, logf); err != nil {
		return false, fmt.Errorf("回滚失败: %w", err)
	}
	return true, nil
}

// stillPending 报告"发起时的那次变更是否仍然处于待确认状态"。
//
// 判据是快照的 CreatedAt：它由发起方在每次变更开始时写入，两次变更不会相同。
// 快照不存在（已被 ClearSnapshot 清掉）或已换成别的变更 → 都不该再回滚。
//
// born 为 nil 时按"说不清、别动手"处理：保险丝的职责是保护网络，
// 而误拉闸的代价是拆掉一条本来好好的隧道 —— 两个方向的代价不对称，
// 所以拿不准时一律不动手。
func stillPending(snapshotPath string, born *Snapshot) bool {
	if born == nil {
		return false
	}
	now, err := Load(snapshotPath)
	if err != nil {
		return false
	}
	return now.CreatedAt.Equal(born.CreatedAt)
}

// Rollback 把网络恢复到"没有 warpgo 隧道"的状态。
//
// 目标是"网络一定能用"，而不是"恢复到变更前一模一样"。
// 后者听起来更贴心，但实现上要保存并复原整套规则，一旦复原过程本身出错，
// 就把唯一的救命通道也搭进去了。回到干净状态是更稳的选择 ——
// 用户看到提示后一条命令就能重新拉起隧道。
func Rollback(snap *Snapshot, logf Logger) error {
	if logf == nil {
		logf = nopLogger
	}
	ifName := netcfg.DefaultIfName
	table := netcfg.TableWarp
	if snap != nil {
		if snap.IfName != "" {
			ifName = snap.IfName
		}
		if snap.Table != 0 {
			table = snap.Table
		}
	}

	logf(EvRollbackRules)
	if _, err := netcfg.Cleanup(nil); err != nil {
		return fmt.Errorf("清理规则: %w", err)
	}

	logf(EvRollbackTable, table)
	for _, fam := range []int{netcfg.FamilyV4, netcfg.FamilyV6} {
		if _, err := netcfg.FlushTable(fam, table); err != nil {
			// 表被别的进程删掉也无所谓，继续走完流程更重要。
			logf(EvNote, fmt.Sprintf("flush table %d (IPv%d): %v", table, fam, err))
		}
	}

	logf(EvRollbackLink, ifName)
	if _, err := netcfg.DeleteLink(ifName); err != nil {
		return fmt.Errorf("删除接口 %s: %w", ifName, err)
	}

	// 校验：默认路由不能再指向我们删掉的接口。
	for _, fam := range []int{netcfg.FamilyV4, netcfg.FamilyV6} {
		dev, err := netcfg.DefaultRouteDev(fam)
		if err != nil {
			logf(EvNote, fmt.Sprintf("check IPv%d default route: %v", fam, err))
			continue
		}
		if dev == "" {
			// v6 没有默认路由是常见情况，不算问题。
			continue
		}
		if dev == ifName {
			return fmt.Errorf("回滚后 IPv%d 的默认路由仍指向 %s", fam, ifName)
		}
	}

	logf(EvRollbackDone)
	return nil
}

// ResolvConfPath 是我们要记录指纹的系统文件。
const ResolvConfPath = "/etc/resolv.conf"

// ResolvFingerprint 返回 resolv.conf 的指纹（文件不存在时返回空串）。
func ResolvFingerprint() string {
	b, err := os.ReadFile(ResolvConfPath)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum)
}

func ensureRunDir() error {
	if err := os.MkdirAll(RunDir(), 0o700); err != nil {
		return fmt.Errorf("创建 %s: %w", RunDir(), err)
	}
	return nil
}

func writeFileAtomic(path string, data []byte) error {
	if err := ensureRunDir(); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("创建临时文件: %w", err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时文件: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("重命名到 %s: %w", path, err)
	}
	return nil
}

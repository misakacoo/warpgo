package cfclient

import (
	"context"
	"fmt"
)

// ApplyMode 把客户端的工作模式设置成 mode。
//
// 它**只负责把模式设对**，不负责 connect 与等待 —— 那两件事在两种模式下
// 的顺序不同（warp 模式要"连上 → 等接口出现 → 加策略规则"），
// 放在调用方更好读，也更好按模式分支。
//
// # 关于 `warp-cli tunnel ip add-range 0.0.0.0/0`
//
// 上游在切到 warp 模式前会先 `tunnel ip add-range 0.0.0.0/0` 与 `::0/0`。
// **这里刻意不做**，理由是这一步的语义是模糊的：`tunnel ip add-range` 加的是
// 分流列表里的条目，而它到底意味着"纳入隧道"还是"排除在外"取决于当前的
// `tunnel mode`（include / exclude）。在本机没有先设 mode 的情况下执行它，
// 结果取决于客户端当时的默认值 —— 也就是说，它可能做的是相反的事，
// 而且**没有任何输出能告诉你**。
//
// 而 `mode warp` 本身就是"整机流量走 WARP"，不需要额外的 range 来促成它。
// 少一个语义不明、结果不可见的动作，就少一处"配置看着对、行为却不是那个"。
func (m *Manager) ApplyMode(ctx context.Context, mode Mode, port int) error {
	switch mode {
	case ModeProxy:
		if err := m.SetMode(ctx, ModeProxy); err != nil {
			return err
		}
		if port <= 0 {
			port = DefaultSocksPort
		}
		return m.SetProxyPort(ctx, port)

	case ModeWarp:
		// 注意：这一步之后客户端的 warp 模式会接管默认路由（整机流量走 WARP），
		// 在远程服务器上**可能切断 SSH**。这是官方客户端 warp 模式的固有行为，
		// 不是我们能关掉的东西 —— 所以调用方必须在动手之前明确警告用户。
		return m.SetMode(ctx, ModeWarp)

	default:
		return fmt.Errorf("不认识的模式 %q", mode)
	}
}

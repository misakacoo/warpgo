package tunnel

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/ipc"
	"golang.zx2c4.com/wireguard/tun"

	"github.com/misakacoo/warpgo/internal/cloudflare"
	"github.com/misakacoo/warpgo/internal/netcfg"
)

// DaemonOptions 是用户态守护进程的启动参数。
type DaemonOptions struct {
	IfName   string
	MTU      int
	Reserved cloudflare.Reserved
	// Verbose 打开 wireguard-go 的逐包日志（排障用，正常不要开）。
	Verbose bool
}

// RunDaemon 是用户态后端的**守护进程主体**：建 TUN、建 wireguard-go 设备、
// 监听 UAPI socket 等着被配置，然后一直活着。
//
// 为什么必须是独立进程：wireguard-go 里"设备的生命周期"就等于"持有 TUN 的那个
// 进程的生命周期"。如果把它嵌进 `warpgo up`，命令一退出隧道就没了 ——
// 而"建立隧道"和"维持隧道"在 CLI 里注定是两次不同的调用。
//
// 它做的事与上游 wireguard-go 的 main() 一致，只有一处不同：这里把 conn.Bind
// 包了一层，好让出网报文带上 reserved（见 reserved.go）。也正因为如此，
// 我们不需要 vendor 一份打过补丁的 wireguard-go —— 上游补丁的全部内容就是
// 在发送前改写那 3 个字节，在那个位置做与在这里做完全等价。
func RunDaemon(opt DaemonOptions) error {
	if strings.TrimSpace(opt.IfName) == "" {
		return errors.New("用户态守护进程需要 --ifname")
	}
	if opt.MTU <= 0 {
		opt.MTU = netcfg.DefaultMTU
	}

	level := device.LogLevelError
	if opt.Verbose {
		level = device.LogLevelVerbose
	}
	logger := device.NewLogger(level, fmt.Sprintf("(%s) ", opt.IfName))
	logger.Verbosef("warpgo 用户态后端启动：mtu=%d reserved=%s", opt.MTU, opt.Reserved)

	tdev, err := tun.CreateTUN(opt.IfName, opt.MTU)
	if err != nil {
		return fmt.Errorf("创建 TUN 设备 %s（需要 /dev/net/tun 与 CAP_NET_ADMIN）: %w", opt.IfName, err)
	}
	realName, err := tdev.Name()
	if err != nil {
		_ = tdev.Close()
		return fmt.Errorf("读取 TUN 设备名: %w", err)
	}

	// UAPI socket 是父进程唯一能配置它的通道，也是 wgctrl 找到它的依据。
	fileUAPI, err := ipc.UAPIOpen(realName)
	if err != nil {
		_ = tdev.Close()
		return fmt.Errorf("创建 UAPI socket（%s/%s.sock）: %w", UAPIDir, realName, err)
	}

	dev := device.NewDevice(tdev, NewReservedBind(conn.NewDefaultBind(), [3]byte(opt.Reserved)), logger)
	if err := dev.Up(); err != nil {
		_ = fileUAPI.Close()
		dev.Close()
		return fmt.Errorf("启动 WireGuard 设备: %w", err)
	}

	uapi, err := ipc.UAPIListen(realName, fileUAPI)
	if err != nil {
		_ = fileUAPI.Close()
		dev.Close()
		return fmt.Errorf("监听 UAPI socket: %w", err)
	}
	logger.Verbosef("设备就绪: %s，UAPI socket 已监听", realName)

	errs := make(chan error, 1)
	go func() {
		for {
			c, aerr := uapi.Accept()
			if aerr != nil {
				errs <- aerr
				return
			}
			go dev.IpcHandle(c)
		}
	}()

	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM, syscall.SIGINT)

	select {
	case sig := <-term:
		logger.Verbosef("收到信号 %v，退出", sig)
	case aerr := <-errs:
		logger.Errorf("UAPI 监听结束: %v", aerr)
	case <-dev.Wait():
		// 接口被删掉（`ip link del warp`）时走到这里。这是守护进程**自己会退出**
		// 的原因 —— 它不会变成一个抱着死 TUN 的僵尸，`down` 也就不必担心
		// "删了接口进程还在"。
		logger.Verbosef("设备已关闭，退出")
	}

	_ = uapi.Close()
	dev.Close()

	// 状态文件由拉起它的那一方写（父进程知道子进程的 PID）；这里只负责在
	// 正常退出时把它摘掉，免得留下一个指向已死进程的记录。
	_ = removeDaemonState(realName)
	return nil
}

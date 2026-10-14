// Package logx 是极简的分级日志。
//
// 为什么不用第三方日志库：本项目刻意保持「零外部依赖」，这样交叉编译和
// 长期维护都没有包袱（对比上游 shell 版本的教训：依赖越多，越容易被上游改动打穿）。
//
// 约定：
//   - 日志一律写 stderr，stdout 只留给机器可读输出（--json）；
//   - 每条一行，便于 journald / grep；
//   - 不使用颜色（会被 systemd 日志污染，且无法重定向到文件阅读）。
package logx

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Level 日志级别。
type Level int

// 级别由低到高。
const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "?"
	}
}

// ParseLevel 解析 "debug"/"info"/"warn"/"error"（大小写不敏感）。
func ParseLevel(s string) (Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return LevelDebug, true
	case "info", "":
		return LevelInfo, true
	case "warn", "warning":
		return LevelWarn, true
	case "error":
		return LevelError, true
	default:
		return LevelInfo, false
	}
}

// Logger 分级日志器，可安全并发使用。
type Logger struct {
	mu    sync.Mutex
	w     io.Writer
	level Level
}

// New 创建日志器。
func New(w io.Writer, level Level) *Logger {
	if w == nil {
		w = os.Stderr
	}
	return &Logger{w: w, level: level}
}

// Default 返回写 stderr、级别 Info 的日志器。
func Default() *Logger { return New(os.Stderr, LevelInfo) }

// Discard 返回一个什么都不写的日志器（测试用）。
func Discard() *Logger { return New(io.Discard, LevelError+1) }

// SetLevel 调整级别。
func (l *Logger) SetLevel(level Level) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = level
}

// Enabled 判断某级别是否会输出。
func (l *Logger) Enabled(level Level) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return level >= l.level
}

func (l *Logger) logf(level Level, format string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if level < l.level {
		return
	}
	ts := time.Now().Format(time.RFC3339)
	fmt.Fprintf(l.w, "%s %-5s %s\n", ts, level, fmt.Sprintf(format, a...))
}

// Debugf 输出 debug 日志。
func (l *Logger) Debugf(format string, a ...any) { l.logf(LevelDebug, format, a...) }

// Infof 输出 info 日志。
func (l *Logger) Infof(format string, a ...any) { l.logf(LevelInfo, format, a...) }

// Warnf 输出 warn 日志。
func (l *Logger) Warnf(format string, a ...any) { l.logf(LevelWarn, format, a...) }

// Errorf 输出 error 日志。
func (l *Logger) Errorf(format string, a ...any) { l.logf(LevelError, format, a...) }

package main

import (
	"errors"
	"strconv"
	"strings"

	"github.com/misakacoo/warpgo/internal/msg"
)

// optionSet 是一个极简选项解析器，只支持三种形态：
//
//	--name            布尔旗标
//	--name value      取值
//	--name=value      取值
//
// 为什么不用标准库 flag：flag 的语法是单横线（`-family`），且遇到第一个非选项
// 参数就停止解析。本项目的命令形态是 `warpgo profile --family 6 --mtu 1280`，
// 自己解析这十几行更直接，也能给出统一的双语错误信息。
type optionSet struct {
	bools map[string]*bool
	strs  map[string]*string
	ints  map[string]*int
}

func newOptionSet() *optionSet {
	return &optionSet{
		bools: map[string]*bool{},
		strs:  map[string]*string{},
		ints:  map[string]*int{},
	}
}

// Bool 注册一个布尔旗标。
func (o *optionSet) Bool(name string, p *bool) *optionSet { o.bools[name] = p; return o }

// Str 注册一个字符串选项。
func (o *optionSet) Str(name string, p *string) *optionSet { o.strs[name] = p; return o }

// Int 注册一个整数选项。
func (o *optionSet) Int(name string, p *int) *optionSet { o.ints[name] = p; return o }

// parse 解析参数，出错时返回可直接展示的文案。
//
// 这里曾经要调用方传一个「把 key 变成当前语言文案」的回调（bundle.Tf）。
// 语言机制移除后它只剩一层空转 —— 函数自己取文案更直接，
// 顺带避免了参数名与包名 `msg` 相撞这种尴尬。
func (o *optionSet) parse(args []string) error {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value, hasValue := splitOption(arg)
		if !strings.HasPrefix(name, "-") {
			return errors.New(msg.Tf(msg.KeyBadOpt, arg))
		}

		if p, ok := o.bools[name]; ok {
			if hasValue {
				return errors.New(msg.Tf(msg.KeyBadOpt, arg))
			}
			*p = true
			continue
		}

		// takeValue 负责取取值：--name=value 直接用，否则吃下一个参数。
		takeValue := func() (string, error) {
			if hasValue {
				return value, nil
			}
			if i+1 < len(args) {
				i++
				return args[i], nil
			}
			return "", errors.New(msg.Tf(msg.KeyNeedArg, name))
		}

		if p, ok := o.strs[name]; ok {
			v, err := takeValue()
			if err != nil {
				return err
			}
			*p = v
			continue
		}
		if p, ok := o.ints[name]; ok {
			v, err := takeValue()
			if err != nil {
				return err
			}
			n, convErr := strconv.Atoi(strings.TrimSpace(v))
			if convErr != nil {
				return errors.New(msg.Tf(msg.KeyBadOpt, arg))
			}
			*p = n
			continue
		}

		return errors.New(msg.Tf(msg.KeyBadOpt, arg))
	}
	return nil
}

// splitOption 把 "--name=value" 拆成 ("--name","value",true)，
// 把 "--name" / "-x" 拆成 ("--name","",false)。
func splitOption(arg string) (name, value string, hasValue bool) {
	if i := strings.IndexByte(arg, '='); i > 0 {
		return arg[:i], arg[i+1:], true
	}
	return arg, "", false
}

// splitList 把逗号/空格分隔的取值切成切片（"1.1.1.1, 1.0.0.1" → 两项）。
func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if v := strings.TrimSpace(f); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// joinOrDash 返回 s；为空时返回 "-"（打印用，避免出现空白字段）。
func joinOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

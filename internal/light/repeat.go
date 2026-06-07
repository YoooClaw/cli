// Package light 移植 phone-notifications 灯效硬件控制面：repeat 归一、线协议编码、
// segments 校验、内置 presets 与发往灯效云 API 的 sender。
package light

import (
	"errors"
	"math"
)

// RepeatInput 对齐 TS RepeatArgument 的「对象」形态：repeat（布尔）/ repeat_times（数字），
// 二者皆可缺省（nil）。
type RepeatInput struct {
	Repeat      *bool
	RepeatTimes *float64
}

// RepeatInputFromAny 从解码后的 JSON 值构造 RepeatInput（非布尔/数字按缺省处理）。
func RepeatInputFromAny(repeat, repeatTimes any) RepeatInput {
	var in RepeatInput
	if b, ok := repeat.(bool); ok {
		in.Repeat = &b
	}
	if n, ok := repeatTimes.(float64); ok {
		in.RepeatTimes = &n
	}
	return in
}

// NormalizeRepeatTimes 把 RepeatInput 归一成 ANCS 语义的 repeat_times：
// repeat_times 优先；其次 repeat（true=0 无限循环 / false=1 一轮）；都缺省则 1。
func NormalizeRepeatTimes(in RepeatInput) (int, error) {
	if in.RepeatTimes != nil {
		return validateRepeatTimes(*in.RepeatTimes)
	}
	if in.Repeat != nil {
		if *in.Repeat {
			return 0, nil
		}
		return 1, nil
	}
	return 1, nil
}

func validateRepeatTimes(value float64) (int, error) {
	if value != math.Trunc(value) || math.IsInf(value, 0) || math.IsNaN(value) || value < 0 {
		return 0, errors.New("repeat_times 必须是 >=0 的整数")
	}
	return int(value), nil
}

// AssertAncsRepeatTimes 限定当前 ANCS 路径仅支持 0（无限循环）或 1（播放一轮）。
func AssertAncsRepeatTimes(repeatTimes int) error {
	if repeatTimes != 0 && repeatTimes != 1 {
		return errors.New("当前 ANCS 路径仅支持 repeat_times=0（无限循环）或 1（播放一轮）；N>=2 需非 ANCS 路径")
	}
	return nil
}

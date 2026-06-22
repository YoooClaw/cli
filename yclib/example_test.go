package yclib_test

import (
	"context"
	"fmt"

	"github.com/YoooClaw/cli/yclib"
)

// Example 展示 Go Agent 进程内集成 yclib 的最小用法：构造 Client → 查询通知。
// 消费方只 import yclib，无需触碰 internal/...（入参/出参类型均由 yclib re-export）。
func Example() {
	// 显式注入配置：profile 空 → "default"；RootDir 空 → ~/.yoooclaw。
	c, err := yclib.New(yclib.Config{Profile: "default"})
	if err != nil {
		panic(err)
	}

	ctx := context.Background()

	// 1) 搜索：按关键字 + 上限过滤，时间倒序。
	res, err := c.Notifications().Search(ctx, yclib.RawQueryOpts{
		Keyword: "会议",
		Limit:   "50",
	})
	if err != nil {
		// 结构化错误：可按错误码分支，而非解析字符串。
		if yclib.ErrorCode(err) == yclib.CodeInvalidArgument {
			fmt.Println("参数非法:", err)
		}
		return
	}
	for _, n := range res.Notifications {
		fmt.Printf("[%s] %s: %s\n", n.AppDisplayName, n.Title, n.Content)
	}

	// 2) 聚合摘要：供 Agent 总结“最近通知概览”。
	sum, _ := c.Notifications().Summary(ctx, yclib.SummaryOpts{Top: 5, Sample: 20})
	fmt.Printf("共 %d 条；Top 应用：%v\n", sum.Total, sum.TopApps)

	// 3) 维度统计：近 7 天按应用聚合。
	stats, _ := c.Notifications().Stats(ctx, yclib.StatsOpts{Dim: "app"})
	fmt.Printf("近 7 天按应用：%v\n", stats.App)
}

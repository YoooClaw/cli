package cli

import (
	"math"
	"strconv"
	"time"

	"github.com/YoooClaw/cli/internal/errs"
	"github.com/spf13/cobra"
)

func addLastDaysFlag(cmd *cobra.Command) {
	cmd.Flags().String("last-days", "", "最近 `N` 天，N 为正整数。以查询时刻为终点，每天按连续 24 小时计算，含起点、不含终点。与显式日期范围互斥")
}

func lastDaysWindow(cmd *cobra.Command) (time.Duration, error) {
	if !cmd.Flags().Changed("last-days") {
		return 0, nil
	}
	days, err := strconv.ParseInt(flagStr(cmd, "last-days"), 10, 64)
	const day = 24 * time.Hour
	if days > math.MaxInt64/int64(day) {
		return 0, errs.New(errs.CodeInvalidArgument, "--last-days 指定的时间范围过大，请缩短天数")
	}
	if err != nil || days <= 0 {
		return 0, errs.New(errs.CodeInvalidArgument, "--last-days 须为正整数")
	}
	for _, flag := range []string{"from", "to", "date", "from-date", "to-date"} {
		if cmd.Flags().Changed(flag) {
			return 0, errs.Newf(errs.CodeInvalidArgument, "--last-days 与 --%s 不能同时使用", flag)
		}
	}
	if flagBool(cmd, "all") {
		return 0, errs.New(errs.CodeInvalidArgument, "--last-days 与 --all 不能同时使用")
	}
	return time.Duration(days) * day, nil
}

func queryBoundaryFlags(cmd *cobra.Command) (string, string, error) {
	window, err := lastDaysWindow(cmd)
	if err != nil {
		return "", "", err
	}
	if window == 0 {
		return flagStr(cmd, "from"), flagStr(cmd, "to"), nil
	}
	// Notification queries accept millisecond precision. Use the same precision across sources.
	end := contextQueryNow().In(time.Local).Truncate(time.Millisecond)
	return end.Add(-window).Format(time.RFC3339Nano), end.Format(time.RFC3339Nano), nil
}

func parseQueryBounds(cmd *cobra.Command, parse func(string, string) (*time.Time, error)) (*time.Time, *time.Time, error) {
	start, end, err := queryBoundaryFlags(cmd)
	if err != nil {
		return nil, nil, err
	}
	from, err := parse(start, "--from")
	if err != nil {
		return nil, nil, err
	}
	to, err := parse(end, "--to")
	return from, to, err
}

package healthdata

import (
	"os"
	"path/filepath"

	"github.com/YoooClaw/cli/internal/fsutil"
)

const dailyReadme = `# 健康日统计

根目录 YYYY-MM-DD.jsonl 按数据所属的当地自然日保存，不按接收日保存。
每个日期文件是一份当前快照。重复同步会更新，不可跨文件或版本重复累计。
第一行是文件说明：schemaVersion/date/timeZone/updatedAt/provider。
其余每行一个指标，只含 metric（稳定业务名）、name（可读名称）、value、unit。
睡眠和运动的多来源处理已经在客户端完成，不要再把文件中的结果按来源相加。

- steps：系统合并后的每日总步数。
- walking_running_distance / cycling_distance：分别为步行跑步/骑行距离，不是所有运动的总距离。
- active_energy：活动能量，不含静息消耗；不要与全天总能量混用。
- flights_climbed：爬楼层数，不是爬升米数。
- apple_exercise_minutes：Apple 锻炼圆环的分钟数，不等于运动会话时长。
- *_average / *_minimum / *_maximum / *_latest：分别为系统日平均/最低/最高/最近值。
- hrv_sdnn_average 与 Android 的 hrv_rmssd_average 算法不同，不可拼成同一趋势。
- oxygen_saturation_average / body_fat_latest：单位 %，98 表示 98%，不是 0.98。
- sleep_minutes：当天 00:00 至次日 00:00 内的实际睡眠分钟，跨午夜按日拆分，不等于“昨夜睡眠”。
- sleep_*_minutes：睡眠阶段或在床/清醒时长；sleep_minutes 已包含睡眠阶段，不能再加阶段时长。
  core 保留核心睡眠语义，不把它强行等同于其他平台的 light。
- workout_*_minutes：按运动开始日期归属的完整运动有效分钟（已扣除原生暂停时长）；跨午夜不按比例猜算。
- workout_sessions：运动次数；不要把 workout_*_minutes 再加到 Apple 锻炼分钟数。

睡眠/运动一期来源规则：按开始时间、结束时间、来源稳定标识排序，第一条记录的来源优先。
睡眠在重叠区间保留优先来源，其他来源只补空白；同源详细阶段优先于未分期睡眠，在床不计入实际睡眠。
运动保留优先来源的完整会话，跳过与它重叠的其他会话，保留不重叠会话。
这个规则不声称复现健康 App 未公开的来源排序。

缺少指标表示此次未读到该项，不能当成 0 或断言用户未授权。
updatedAt 是最后一次采集时刻；今天是截至该时刻的部分日。
系统统计可能随同步、源记录删除或权限变化而更新，跟踪应读取当前文件。
旧协议原始批次在 legacy/receipts；首次接收日快照时归档的旧根目录文件完整备份在 legacy/imported。
legacy 只用于兼容与排查，不能把这些原始记录再次累计到日统计中。
`

func (s *Storage) ensureDailyReadme() error {
	path := filepath.Join(s.dir, "README.md")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return fsutil.WriteAtomic(path, []byte(dailyReadme), fsutil.SecretFileMode)
}

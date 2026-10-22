package healthdata

import (
	"os"
	"path/filepath"

	"github.com/YoooClaw/cli/internal/fsutil"
)

const dailyReadme = `# 健康数据（v3）

根目录 YYYY-MM-DD.jsonl 是一份日期快照，重复同步覆盖更新，不可把不同版本累加。
第一行是 schemaVersion=3/date/timeZone/updatedAt/provider；后续行分两类：
- 普通指标：metric/name/value/unit。步数、能量、心率等按当地自然日统计。
- 睡眠：type=sleepSession，session 内保存一次完整睡眠的摘要与全部 segments。

睡眠会话归记录结束的当地日期，跨午夜不拆分；同日可以有夜间睡眠和午睡等多次记录。
startAt/endAt 是记录到的首个/末个实际睡眠片段边界，不代表上床时间或确认实际起床。
客户端先按开始/结束时间及来源标识排序，优先来源覆盖重叠，其他来源只补空白；
同源详细阶段优先于 unspecified。相邻实际睡眠片段间隔 <=60 分钟归为同一会话。
这是产品规则，不声称复现 Apple 未公开的来源排序或会话识别算法。

session 字段：id/startAt/endAt/sleepMinutes/stageMinutes/awakeMinutes/awakeningCount/
longestAwakeMinutes/unknownMinutes/isPartial/sources/segments。
stageMinutes 包含 core/deep/rem/unspecified；sleepMinutes 为四类之和。
segments 按时间顺序保存 stage/startAt/endAt/source；时间保留毫秒及偏移。
awake 为有记录的清醒；unknown 为记录空白，不算睡眠或清醒。
inBed 不作为睡眠阶段进入此时间线，也不计入睡眠时长。
统计先将每个有效片段的起止时刻分别四舍五入到分钟，再相减汇总；30 秒进位，短片段可为 0。
awakeningCount 按原始连续 Awake 区间计数，可能包含取整后时长为 0 的片段。
isPartial 表示触及查询边界；false 也不代表已经确认用户起床或设备数据全部同步。
sources 是来源标识与名称；同一来源已合并，不可再次按来源累加。

Agent 默认读取摘要，问到阶段先后或具体清醒时间时再读取 segments。
GET /assets/health-data 返回每日指标及 sleepSessions 摘要，不返回全部时间线。
GET /assets/health-data/{date}/sleep-sessions/{id} 通过 revision 与 limit/cursor 分页读取阶段。

普通指标说明：steps 为总步数，active_energy 不含静息消耗；oxygen_saturation_average/
body_fat_latest 使用百分数（98 表示98%）；hrv_sdnn 与其他平台 RMSSD 不可混算。
workout_*_minutes 是按开始日归属的完整运动有效时长，workout_sessions 是次数；
不要与 apple_exercise_minutes 重复累计。数量和运动仍由客户端合并来源。

缺少指标不能当成0或断言权限拒绝。updatedAt 是查询开始的采集时刻，今天为部分日。
仅支持 v3；旧格式文件明确报错，不迁移。开发环境应先单独备份旧数据，再重新同步。
睡眠id在起点或来源变更时可能变化，应替换整个日期快照，不追加累加。
`

func (s *Storage) ensureDailyReadme() error {
	path := filepath.Join(s.dir, "README.md")
	if raw, err := os.ReadFile(path); err == nil && string(raw) == dailyReadme {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	return fsutil.WriteAtomic(path, []byte(dailyReadme), fsutil.SecretFileMode)
}

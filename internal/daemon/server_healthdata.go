package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/YoooClaw/cli/internal/healthdata"
	"github.com/YoooClaw/cli/internal/relay"
)

// handleHealthDataIngest 接收 v3 日指标与完整睡眠会话快照。
func (s *server) handleHealthDataIngest(w http.ResponseWriter, r *http.Request) {
	var raw json.RawMessage
	if !decodeBody(w, r, &raw) {
		return
	}
	batch, err := healthdata.Prepare(raw)
	if err != nil {
		s.logger.Warn(fmt.Sprintf("[health-data] requestId=%s rejected: %v", r.Header.Get(relay.InternalRequestIDHeader), err))
		writeJSON(w, http.StatusBadRequest, errBody("INVALID_PARAMS", err.Error()))
		return
	}
	if collectedAtTooFarInFuture(batch.CollectedAt()) {
		s.logger.Warn(fmt.Sprintf("[health-data] requestId=%s rejected: collectedAt is too far in the future", r.Header.Get(relay.InternalRequestIDHeader)))
		writeJSON(w, http.StatusBadRequest, errBody("INVALID_PARAMS", "collectedAt is too far in the future"))
		return
	}
	days, err := s.healthDataStorage.Append(batch)
	rejected, dropped := 0, 0
	for _, day := range days {
		dropped += day.Dropped
		if !day.Applied && day.Reason != "apple_priority" && day.Reason != "stale_collected_at" {
			rejected++
		}
		if day.Applied {
			// 类别结构错误只产生 warning 而没有可计数的记录，也要留痕。
			if day.Dropped > 0 || len(day.Warnings) > 0 {
				s.logger.Warn(fmt.Sprintf("[health-data] requestId=%s date=%s applied=true dropped=%d warnings=%s",
					r.Header.Get(relay.InternalRequestIDHeader), day.Date, day.Dropped, strings.Join(day.Warnings, "; ")))
			} else {
				s.logger.Info(fmt.Sprintf("[health-data] requestId=%s date=%s applied=true reason=%s",
					r.Header.Get(relay.InternalRequestIDHeader), day.Date, day.Reason))
			}
			continue
		}
		// 被忽略的天（来源优先级/采集时间更旧）不是故障；校验或读取失败才需要告警。
		if day.Reason == "apple_priority" || day.Reason == "stale_collected_at" {
			s.logger.Info(fmt.Sprintf("[health-data] requestId=%s date=%s applied=false reason=%s",
				r.Header.Get(relay.InternalRequestIDHeader), day.Date, day.Reason))
			continue
		}
		s.logger.Warn(fmt.Sprintf("[health-data] requestId=%s date=%s applied=false reason=%s message=%s dropped=%d warnings=%s",
			r.Header.Get(relay.InternalRequestIDHeader), day.Date, day.Reason, day.Message, day.Dropped, strings.Join(day.Warnings, "; ")))
	}
	if err != nil {
		s.logger.Error(fmt.Sprintf("[health-data] requestId=%s write failed: %v", r.Header.Get(relay.InternalRequestIDHeader), err))
		writeJSON(w, http.StatusInternalServerError, errBody("HEALTH_DATA_WRITE_FAILED", "健康数据保存失败"))
		return
	}
	s.logger.Info(fmt.Sprintf("[health-data] requestId=%s ingested=%d deleted=%d rejected=%d dropped=%d",
		r.Header.Get(relay.InternalRequestIDHeader), batch.ItemCount, batch.DeletedCount, rejected, dropped))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ingested": batch.ItemCount, "deleted": batch.DeletedCount, "rejected": rejected, "days": days})
}

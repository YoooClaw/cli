package daemon

import (
	"context"
	"errors"

	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/transfercloud"
)

func (s *server) recordingURLRefresher(label, taskID string) func(context.Context) (string, error) {
	keyForLabel := func() string {
		set := s.snapshotCreds()
		for _, e := range set.Entries {
			if e.Label == label {
				return e.Key
			}
		}
		if label == "local" && set.DefaultEntry != nil {
			return set.DefaultEntry.Key
		}
		return ""
	}
	key := keyForLabel()
	host := config.ResolveCloudHost(s.cfg)
	valid := func() bool { return key != "" && keyForLabel() == key && config.ResolveCloudHost(s.cfg) == host }
	return func(ctx context.Context) (string, error) {
		if !valid() {
			return "", errors.New("recording refresh credentials missing or changed")
		}
		next, err := transfercloud.New(key, host).RecordingDownloadURL(ctx, taskID)
		if !valid() {
			return "", errors.New("recording refresh credentials changed")
		}
		return next, err
	}
}

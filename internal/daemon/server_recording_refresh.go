package daemon

import (
	"context"
	"errors"

	"github.com/YoooClaw/cli/internal/clientlabel"
	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/recording"
	"github.com/YoooClaw/cli/internal/transfercloud"
)

func (s *server) recordingKeyForLabel(label string) string {
	set := s.snapshotCreds()
	for _, e := range set.Entries {
		if e.Label == label {
			return e.Key
		}
	}
	if (label == "local" || clientlabel.Shared(label)) && set.DefaultEntry != nil {
		return set.DefaultEntry.Key
	}
	return ""
}

func (s *server) recordingURLRefresher(label, taskID string) func(context.Context) (recording.DownloadLink, error) {
	keyForLabel := func() string { return s.recordingKeyForLabel(label) }
	key := keyForLabel()
	host := config.ResolveCloudHost(s.cfg)
	valid := func() bool { return key != "" && keyForLabel() == key && config.ResolveCloudHost(s.cfg) == host }
	return func(ctx context.Context) (recording.DownloadLink, error) {
		if !valid() {
			return recording.DownloadLink{}, errors.New("recording refresh credentials missing or changed")
		}
		next, err := transfercloud.New(key, host).RecordingDownloadLink(ctx, taskID)
		if !valid() {
			return recording.DownloadLink{}, errors.New("recording refresh credentials changed")
		}
		if err != nil {
			return recording.DownloadLink{}, err
		}
		return recording.DownloadLink{URL: next.URL, ExpiresAt: next.ExpiresAt}, nil
	}
}

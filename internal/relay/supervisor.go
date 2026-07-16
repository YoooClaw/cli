package relay

import (
	"path/filepath"
	"sync"

	"github.com/YoooClaw/cli/internal/creds"
)

// SupervisorOptions 是多隧道 supervisor 配置。
type SupervisorOptions struct {
	TunnelURL          string
	HTTPBaseURL        string
	HTTPToken          string
	HeartbeatSec       int
	ReconnectBackoffMs int
	StateDir           string
	Logger             Logger
}

type managedTunnel struct {
	entry      creds.ApiKeyEntry
	client     *Client
	dispatcher *Dispatcher
}

// Supervisor 管理每个 api-key label 对应的一条 Relay 隧道。
type Supervisor struct {
	opts SupervisorOptions

	mu           sync.Mutex
	tunnels      map[string]*managedTunnel
	defaultLabel string
	mode         string
}

// NewSupervisor 构造 supervisor。
func NewSupervisor(opts SupervisorOptions) *Supervisor {
	return &Supervisor{opts: opts, tunnels: map[string]*managedTunnel{}}
}

// Apply 增量应用 CredentialSet。
func (s *Supervisor) Apply(set creds.CredentialSet) ApplyResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := ApplyResult{}
	next := map[string]creds.ApiKeyEntry{}
	for _, entry := range set.Entries {
		next[entry.Label] = entry
	}
	for label, managed := range s.tunnels {
		nextEntry, ok := next[label]
		if !ok {
			managed.client.Stop("removed")
			managed.dispatcher.Cleanup()
			delete(s.tunnels, label)
			result.Stopped = append(result.Stopped, label)
			continue
		}
		if nextEntry.Key != managed.entry.Key {
			managed.client.Stop("key-changed")
			managed.dispatcher.Cleanup()
			s.tunnels[label] = s.startLocked(nextEntry)
			result.Restarted = append(result.Restarted, label)
		} else {
			managed.entry = nextEntry
			result.Unchanged = append(result.Unchanged, label)
		}
	}
	for _, entry := range set.Entries {
		if _, ok := s.tunnels[entry.Label]; ok {
			continue
		}
		s.tunnels[entry.Label] = s.startLocked(entry)
		result.Started = append(result.Started, entry.Label)
	}
	s.mode = set.Mode
	s.defaultLabel = ""
	if set.DefaultEntry != nil {
		s.defaultLabel = set.DefaultEntry.Label
	}
	return result
}

// StopAll 停止全部隧道。
func (s *Supervisor) StopAll(reason string) {
	s.mu.Lock()
	tunnels := s.tunnels
	s.tunnels = map[string]*managedTunnel{}
	s.mu.Unlock()
	for _, managed := range tunnels {
		managed.client.Stop(reason)
		managed.dispatcher.Cleanup()
	}
}

// Reconnect 重启指定 label 或全部隧道。
func (s *Supervisor) Reconnect(label string) (reconnected []string, missing string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	labels := []string{}
	if label != "" {
		labels = []string{label}
	} else {
		for item := range s.tunnels {
			labels = append(labels, item)
		}
	}
	for _, item := range labels {
		managed := s.tunnels[item]
		if managed == nil {
			return reconnected, item
		}
		managed.client.Stop("manual-reconnect")
		managed.dispatcher.Cleanup()
		s.tunnels[item] = s.startLocked(managed.entry)
		reconnected = append(reconnected, item)
	}
	return reconnected, ""
}

// PushEvent 给所有已管理隧道广播事件。
func (s *Supervisor) PushEvent(event string, payload any) {
	s.mu.Lock()
	tunnels := make([]*managedTunnel, 0, len(s.tunnels))
	for _, managed := range s.tunnels {
		tunnels = append(tunnels, managed)
	}
	s.mu.Unlock()
	for _, managed := range tunnels {
		managed.dispatcher.PushEvent(event, payload)
	}
}

// Status 返回所有隧道状态。
func (s *Supervisor) Status() SupervisorStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := SupervisorStatus{Mode: s.mode, DefaultLabel: s.defaultLabel}
	for _, managed := range s.tunnels {
		status := managed.client.Status()
		out.Tunnels = append(out.Tunnels, TunnelStatus{
			Label: managed.entry.Label, Default: managed.entry.Label == s.defaultLabel,
			Connected: status.Connected, URL: status.URL, ReconnectAttempt: status.ReconnectAttempt,
			LastDisconnectReason: status.LastDisconnectReason,
		})
	}
	return out
}

// DefaultStatus 返回默认隧道状态，没有默认则返回第一条。
func (s *Supervisor) DefaultStatus() *Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	if managed := s.tunnels[s.defaultLabel]; managed != nil {
		status := managed.client.Status()
		return &status
	}
	for _, managed := range s.tunnels {
		status := managed.client.Status()
		return &status
	}
	return nil
}

func (s *Supervisor) startLocked(entry creds.ApiKeyEntry) *managedTunnel {
	label := entry.Label
	logger := prefixedLogger{Logger: s.opts.Logger, Prefix: "[tunnel:" + label + "] "}
	client := NewClient(ClientOptions{
		TunnelURL: s.opts.TunnelURL,
		CredentialProvider: func() (Credential, error) {
			return relayCredential(entry.Key), nil
		},
		HeartbeatSec:       s.opts.HeartbeatSec,
		ReconnectBackoffMs: s.opts.ReconnectBackoffMs,
		StatusFilePath:     filepath.Join(s.opts.StateDir, "state", "tunnels", label+".json"),
		Logger:             logger,
	})
	dispatcher := NewDispatcher(DispatcherOptions{
		Client: client, HTTPBaseURL: s.opts.HTTPBaseURL, HTTPToken: s.opts.HTTPToken,
		ClientLabel: label, Logger: logger,
	})
	dispatcher.Start()
	client.OnDisconnected(func(reason string) {
		logger.Info("Relay tunnel disconnected; cleaning local WS channels: " + reason)
		dispatcher.Cleanup()
	})
	client.Start()
	s.opts.Logger.Info("[tunnel:" + label + "] Relay tunnel started")
	return &managedTunnel{entry: entry, client: client, dispatcher: dispatcher}
}

type prefixedLogger struct {
	Logger
	Prefix string
}

func (l prefixedLogger) Debug(msg string) { l.Logger.Debug(l.Prefix + msg) }
func (l prefixedLogger) Info(msg string)  { l.Logger.Info(l.Prefix + msg) }
func (l prefixedLogger) Warn(msg string)  { l.Logger.Warn(l.Prefix + msg) }
func (l prefixedLogger) Error(msg string) { l.Logger.Error(l.Prefix + msg) }

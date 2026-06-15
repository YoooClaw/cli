package creds

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/paths"
)

const (
	APIKeyEnv             = "YOOOCLAW_API_KEY"
	KeychainService       = "yoooclaw"
	KeychainAPIKeyAccount = "api-key"
	apiKeyField           = "apiKey"
	apiKeysField          = "apiKeys"
)

var (
	reservedLabels = map[string]bool{"all": true, "legacy": true, "env": true, "keychain": true, "local": true}
	labelRE        = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
)

// ApiKeyEntry 是运行时 api-key 条目。
type ApiKeyEntry struct {
	Label     string
	Key       string
	Default   bool
	Source    string // env | keychain | file
	CreatedAt string
}

// ApiKeyResolution 是单个 api-key 解析结果。
type ApiKeyResolution struct {
	Value    string
	Source   string // env | keychain | file | none
	Location string
	Label    string
}

// CredentialSet 是 account 级 api-key 的整体解析结果。
type CredentialSet struct {
	Mode                    string
	Entries                 []ApiKeyEntry
	DefaultEntry            *ApiKeyEntry
	Location                string
	LegacyAPIKeyPresent     bool
	ShadowedKeychainPresent bool
	Warnings                []string
}

type storedEntry struct {
	Label     string
	Key       string
	Default   bool
	CreatedAt string
}

// IsValidAPIKeyLabel 校验 label 合法性。
func IsValidAPIKeyLabel(label string) bool {
	return labelRE.MatchString(label) && !reservedLabels[label]
}

// AssertValidAPIKeyLabel 校验失败时返回错误。
func AssertValidAPIKeyLabel(label string) error {
	if !IsValidAPIKeyLabel(label) {
		return errs.New(errs.CodeAPIKeyLabelInvalid,
			"label 不合法：仅允许 [a-z0-9-]{1,32}，且不能使用保留字 all/legacy/env/keychain/local",
			map[string]any{"label": label})
	}
	return nil
}

func readSharedCredentials() map[string]any {
	data := map[string]any{}
	_, _ = fsutil.ReadJSON(paths.SharedCredentialsPath(), &data)
	if data == nil {
		data = map[string]any{}
	}
	return data
}

func writeSharedCredentials(data map[string]any) error {
	return fsutil.WriteJSON(paths.SharedCredentialsPath(), data, fsutil.SecretFileMode)
}

func normalizeStoredEntries(raw any, warnings *[]string) []storedEntry {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	var out []storedEntry
	for _, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok {
			*warnings = append(*warnings, "apiKeys[] 包含非对象条目，已跳过")
			continue
		}
		label, _ := obj["label"].(string)
		label = strings.TrimSpace(label)
		key, _ := obj["key"].(string)
		key = strings.TrimSpace(key)
		if !IsValidAPIKeyLabel(label) {
			disp := label
			if disp == "" {
				disp = "<empty>"
			}
			*warnings = append(*warnings, "apiKeys[] 包含非法 label: "+disp+"，已跳过")
			continue
		}
		if key == "" {
			*warnings = append(*warnings, "apiKeys[] label="+label+" 的 key 为空，已跳过")
			continue
		}
		if seen[label] {
			*warnings = append(*warnings, "apiKeys[] label="+label+" 重复，已跳过后续条目")
			continue
		}
		seen[label] = true
		createdAt, _ := obj["createdAt"].(string)
		out = append(out, storedEntry{
			Label:     label,
			Key:       key,
			Default:   obj["default"] == true,
			CreatedAt: createdAt,
		})
	}
	return out
}

func toRuntimeEntries(entries []storedEntry) []ApiKeyEntry {
	defaultIdx := -1
	for i, e := range entries {
		if e.Default {
			defaultIdx = i
			break
		}
	}
	effective := -1
	if len(entries) > 0 {
		if defaultIdx >= 0 {
			effective = defaultIdx
		} else {
			effective = 0
		}
	}
	out := make([]ApiKeyEntry, len(entries))
	for i, e := range entries {
		out[i] = ApiKeyEntry{Label: e.Label, Key: e.Key, Default: i == effective, Source: "file", CreatedAt: e.CreatedAt}
	}
	return out
}

func pickDefault(entries []ApiKeyEntry) *ApiKeyEntry {
	for i := range entries {
		if entries[i].Default {
			return &entries[i]
		}
	}
	if len(entries) > 0 {
		return &entries[0]
	}
	return nil
}

func keychainValue() string {
	if !keychainAvailableFn() {
		return ""
	}
	return keychainGetFn(KeychainService, KeychainAPIKeyAccount).Value
}

func storedEntriesFromFile(data map[string]any) []storedEntry {
	var warnings []string
	return normalizeStoredEntries(data[apiKeysField], &warnings)
}

func writeAPIKeys(entries []storedEntry) (ApiKeyResolution, error) {
	data := readSharedCredentials()
	arr := make([]any, 0, len(entries))
	for _, e := range entries {
		obj := map[string]any{"label": e.Label, "key": e.Key}
		if e.Default {
			obj["default"] = true
		}
		if e.CreatedAt != "" {
			obj["createdAt"] = e.CreatedAt
		}
		arr = append(arr, obj)
	}
	data[apiKeysField] = arr
	delete(data, apiKeyField)
	if err := writeSharedCredentials(data); err != nil {
		return ApiKeyResolution{}, err
	}
	runtime := toRuntimeEntries(entries)
	chosen := pickDefault(runtime)
	res := ApiKeyResolution{Source: "none", Location: paths.SharedCredentialsPath()}
	if chosen != nil {
		res.Value, res.Source, res.Label = chosen.Key, chosen.Source, chosen.Label
	}
	return res, nil
}

// ResolveAPIKeyEntries 解析 account 级 api-key——仅从 credentials.json 读取；
// 环境变量 YOOOCLAW_API_KEY 与 keychain 不再参与解析（keychain 若存在仅作 shadowed 提示）。
func ResolveAPIKeyEntries() CredentialSet {
	file := paths.SharedCredentialsPath()

	data := readSharedCredentials()
	legacyRaw, _ := data[apiKeyField].(string)
	legacy := strings.TrimSpace(legacyRaw) != ""
	kc := keychainValue()

	if _, ok := data[apiKeysField]; ok {
		var warnings []string
		stored := normalizeStoredEntries(data[apiKeysField], &warnings)
		hasDefault := false
		for _, e := range stored {
			if e.Default {
				hasDefault = true
				break
			}
		}
		if len(stored) > 0 && !hasDefault {
			warnings = append(warnings, "apiKeys[] 未标记 default，运行时使用第一条 label="+stored[0].Label)
		}
		entries := toRuntimeEntries(stored)
		return CredentialSet{
			Mode: "file-multi", Entries: entries, DefaultEntry: pickDefault(entries), Location: file,
			LegacyAPIKeyPresent: legacy, ShadowedKeychainPresent: kc != "", Warnings: warnings,
		}
	}

	if legacy {
		val := strings.TrimSpace(legacyRaw)
		entry := ApiKeyEntry{Label: "default", Key: val, Default: true, Source: "file"}
		return CredentialSet{Mode: "legacy-file-single", Entries: []ApiKeyEntry{entry}, DefaultEntry: &entry,
			Location: file, LegacyAPIKeyPresent: true, ShadowedKeychainPresent: kc != ""}
	}

	return CredentialSet{Mode: "none", Location: file, ShadowedKeychainPresent: kc != ""}
}

// ResolveAPIKey 返回当前默认 api-key。
func ResolveAPIKey() ApiKeyResolution {
	set := ResolveAPIKeyEntries()
	if set.DefaultEntry == nil {
		return ApiKeyResolution{Source: "none", Location: set.Location}
	}
	e := set.DefaultEntry
	return ApiKeyResolution{Value: e.Key, Source: e.Source, Location: set.Location, Label: e.Label}
}

// SetAPIKey 写入 account 级 api-key。默认写共享文件；useKeychain 时写 OS keychain。
func SetAPIKey(key string, useKeychain bool) (ApiKeyResolution, error) {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return ApiKeyResolution{}, errs.New(errs.CodeInvalidArgument, "api-key 不能为空")
	}
	if useKeychain {
		data := readSharedCredentials()
		if _, ok := data[apiKeysField]; ok {
			return ApiKeyResolution{}, errs.New(errs.CodeKeychainMultiUnsupport,
				"multi-key 模式不支持 --keychain；请使用文件 apiKeys[] 管理多 key")
		}
		if !keychainSetFn(KeychainService, KeychainAPIKeyAccount, trimmed) {
			return ApiKeyResolution{}, errs.New(errs.CodeKeychainUnavailable,
				"当前平台无法写入 keychain，请去掉 --keychain 改写共享文件")
		}
		return ApiKeyResolution{Value: trimmed, Source: "keychain",
			Location: KeychainService + "/" + KeychainAPIKeyAccount, Label: "keychain"}, nil
	}
	data := readSharedCredentials()
	if _, ok := data[apiKeysField]; ok {
		entries := storedEntriesFromFile(data)
		if len(entries) == 0 {
			return writeAPIKeys([]storedEntry{{Label: "default", Key: trimmed, Default: true, CreatedAt: nowISO()}})
		}
		idx := 0
		for i, e := range entries {
			if e.Default {
				idx = i
				break
			}
		}
		for i := range entries {
			entries[i].Default = i == idx
			if i == idx {
				entries[i].Key = trimmed
			}
		}
		return writeAPIKeys(entries)
	}
	data[apiKeyField] = trimmed
	if err := writeSharedCredentials(data); err != nil {
		return ApiKeyResolution{}, err
	}
	return ApiKeyResolution{Value: trimmed, Source: "file", Location: paths.SharedCredentialsPath(), Label: "default"}, nil
}

// AddAPIKey 新增一条 multi-key api-key。
func AddAPIKey(key, label string, makeDefault, force bool) (ApiKeyResolution, error) {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return ApiKeyResolution{}, errs.New(errs.CodeInvalidArgument, "api-key 不能为空")
	}
	if err := AssertValidAPIKeyLabel(label); err != nil {
		return ApiKeyResolution{}, err
	}
	data := readSharedCredentials()
	var entries []storedEntry
	if _, ok := data[apiKeysField]; ok {
		entries = storedEntriesFromFile(data)
	} else if legacy := strings.TrimSpace(getString(data, apiKeyField)); legacy != "" {
		entries = []storedEntry{{Label: "default", Key: legacy, Default: true, CreatedAt: nowISO()}}
	}

	existingIdx := indexOfLabel(entries, label)
	if existingIdx >= 0 && !force {
		return ApiKeyResolution{}, errs.New(errs.CodeAPIKeyLabelDuplicate, "api-key label 已存在："+label,
			map[string]any{"hint": "如需覆盖，追加 --force", "label": label})
	}

	shouldDefault := makeDefault || len(entries) == 0
	created := nowISO()
	if existingIdx >= 0 {
		created = entries[existingIdx].CreatedAt
	}
	next := storedEntry{Label: label, Key: trimmed, Default: shouldDefault, CreatedAt: created}
	if existingIdx >= 0 {
		entries[existingIdx] = next
	} else {
		entries = append(entries, next)
	}
	if shouldDefault {
		for i := range entries {
			entries[i].Default = entries[i].Label == label
		}
	} else {
		hasDefault := false
		for _, e := range entries {
			if e.Default {
				hasDefault = true
				break
			}
		}
		if !hasDefault && len(entries) > 0 {
			entries[0].Default = true
		}
	}
	return writeAPIKeys(entries)
}

// RemoveAPIKey 删除指定 label 的 api-key。
func RemoveAPIKey(label string) (res ApiKeyResolution, removed, newDefault string, err error) {
	if err = AssertValidAPIKeyLabel(label); err != nil {
		return
	}
	data := readSharedCredentials()
	var entries []storedEntry
	if _, ok := data[apiKeysField]; ok {
		entries = storedEntriesFromFile(data)
	} else if legacy := strings.TrimSpace(getString(data, apiKeyField)); legacy != "" {
		entries = []storedEntry{{Label: "default", Key: legacy, Default: true, CreatedAt: nowISO()}}
	}
	idx := indexOfLabel(entries, label)
	if idx < 0 {
		err = errs.New(errs.CodeAPIKeyLabelNotFound, "api-key label 不存在："+label, map[string]any{"label": label})
		return
	}
	removedDefault := entries[idx].Default
	var next []storedEntry
	for _, e := range entries {
		if e.Label != label {
			next = append(next, e)
		}
	}
	hasDefault := false
	for _, e := range next {
		if e.Default {
			hasDefault = true
			break
		}
	}
	if len(next) > 0 && (removedDefault || !hasDefault) {
		for i := range next {
			next[i].Default = i == 0
		}
	}
	res, err = writeAPIKeys(next)
	return res, label, res.Label, err
}

// SetDefaultAPIKey 切换 default api-key。
func SetDefaultAPIKey(label string) (ApiKeyResolution, error) {
	if err := AssertValidAPIKeyLabel(label); err != nil {
		return ApiKeyResolution{}, err
	}
	data := readSharedCredentials()
	var entries []storedEntry
	if _, ok := data[apiKeysField]; ok {
		entries = storedEntriesFromFile(data)
	}
	if indexOfLabel(entries, label) < 0 {
		return ApiKeyResolution{}, errs.New(errs.CodeAPIKeyLabelNotFound, "api-key label 不存在："+label, map[string]any{"label": label})
	}
	for i := range entries {
		entries[i].Default = entries[i].Label == label
	}
	return writeAPIKeys(entries)
}

// MaskSecret 遮罩密钥用于展示。
func MaskSecret(value string) string {
	if value == "" {
		return ""
	}
	if len(value) <= 8 {
		return "***"
	}
	return value[:4] + "***" + value[len(value)-4:]
}

// ResolveGatewayToken 解析当前 profile 的 gateway token（按 config.auth.tokenRef）。
func ResolveGatewayToken(cfg config.Config) (ResolvedRef, error) {
	return ResolveRef(cfg.Auth.TokenRef)
}

// WriteGatewayToken 写入/轮换 gateway token。
func WriteGatewayToken(cfg config.Config, value string) (ResolvedRef, error) {
	return WriteRef(cfg.Auth.TokenRef, value)
}

// GenerateToken 生成一个随机 token（hex）。
func GenerateToken(numBytes int) string {
	if numBytes <= 0 {
		numBytes = 32
	}
	b := make([]byte, numBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func indexOfLabel(entries []storedEntry, label string) int {
	for i, e := range entries {
		if e.Label == label {
			return i
		}
	}
	return -1
}

func getString(data map[string]any, key string) string {
	s, _ := data[key].(string)
	return s
}

func nowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

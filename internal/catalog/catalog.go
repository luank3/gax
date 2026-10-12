// Package catalog resolves model metadata from user overrides and a models.dev snapshot.
package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/luank3/gax/internal/config"
)

const MaxTiers = 4

type Support int8

const (
	SupportUnknown Support = -1
	SupportNo      Support = 0
	SupportYes     Support = 1
)

type Tier struct {
	ContextThreshold int64
	CostInput        float64
	CostOutput       float64
	CostCacheRead    float64
	CostCacheWrite   float64
	CostCacheWrite1H float64
}

// Entry contains pricing and capability metadata for a model. Negative rates and zero limits
// represent unknown values. Declared flags preserve meaningful explicit empty/off values.
type Entry struct {
	CostInput           float64
	CostOutput          float64
	CostCacheRead       float64
	CostCacheWrite      float64
	CostCacheWrite1H    float64
	ContextWindow       int64
	MaxOutput           int64
	ImageInput          Support
	API                 string
	Efforts             []string
	EffortsKnown        bool
	InterleavedField    string
	InterleavedDeclared bool
	Tiers               []Tier
	TiersDeclared       bool
}

type Catalog struct {
	store      *config.Store
	snapshotMu sync.RWMutex
	snapshot   map[string]json.RawMessage

	fetchMu             sync.Mutex
	fetchAttempted      bool
	fetch               *fetchState
	cacheGeneration     uint64
	staleDaysUnreported int64
}

func New(store *config.Store) *Catalog { return &Catalog{store: store} }

func unknownEntry() Entry {
	return Entry{
		CostInput: -1, CostOutput: -1, CostCacheRead: -1, CostCacheWrite: -1,
		CostCacheWrite1H: -1, ImageInput: SupportUnknown,
	}
}

// LoadSnapshot validates a provider-shaped models.dev artifact before replacing the current
// snapshot. Invalid downloads leave the last good snapshot available.
func (catalog *Catalog) LoadSnapshot(data []byte) error {
	if catalog == nil {
		return errors.New("catalog is nil")
	}
	providers, err := parseSnapshot(data)
	if err != nil {
		return err
	}
	catalog.snapshotMu.Lock()
	catalog.snapshot = providers
	catalog.snapshotMu.Unlock()
	return nil
}

func parseSnapshot(data []byte) (map[string]json.RawMessage, error) {
	providers, ok := decodeObject(data)
	if !ok {
		return nil, errors.New("catalog snapshot must be a JSON object")
	}
	valid := false
	for _, raw := range providers {
		provider, ok := decodeObject(raw)
		if !ok {
			continue
		}
		if _, ok := decodeObject(provider["models"]); ok {
			valid = true
			break
		}
	}
	if !valid {
		return nil, errors.New("catalog snapshot contains no provider models object")
	}
	return cloneRawObject(providers), nil
}

// Lookup resolves provider-scoped configuration, then catalog-ID configuration, then the
// snapshot indexed by catalogID. Metadata merges field by field in that precedence order.
func (catalog *Catalog) Lookup(providerID, catalogID, model string) (Entry, bool) {
	entry := unknownEntry()
	if model == "" {
		return entry, false
	}
	catalog.fillConfig(providerID, model, &entry)
	if catalogID != "" && catalogID != providerID {
		catalog.fillConfig(catalogID, model, &entry)
	}
	if catalogID != "" {
		catalog.fillSnapshot(catalogID, model, &entry)
	}
	return entry, entry.hasMetadata()
}

// LookupConfig resolves the two config scopes without consulting the snapshot.
func (catalog *Catalog) LookupConfig(providerID, catalogID, model string) (Entry, bool) {
	entry := unknownEntry()
	if model == "" {
		return entry, false
	}
	catalog.fillConfig(providerID, model, &entry)
	if catalogID != "" && catalogID != providerID {
		catalog.fillConfig(catalogID, model, &entry)
	}
	return entry, entry.hasMetadata()
}

func (catalog *Catalog) RoutesModels(providerID string) bool {
	if catalog == nil || catalog.store == nil || providerID == "" {
		return false
	}
	root, ok := decodeObject(catalog.store.JSONNode("catalog.models"))
	if !ok {
		return false
	}
	provider, ok := decodeObject(root[providerID])
	if !ok {
		return false
	}
	for _, raw := range provider {
		model, ok := decodeObject(raw)
		if !ok {
			continue
		}
		if _, ok := jsonString(model["api"]); ok {
			return true
		}
	}
	return false
}

func (catalog *Catalog) fillConfig(providerID, model string, entry *Entry) {
	if catalog == nil || catalog.store == nil || providerID == "" {
		return
	}
	root, ok := decodeObject(catalog.store.JSONNode("catalog.models"))
	if !ok {
		return
	}
	provider, ok := decodeObject(root[providerID])
	if !ok {
		return
	}
	fillModel(provider[model], entry)
}

func (catalog *Catalog) fillSnapshot(providerID, model string, entry *Entry) {
	if catalog == nil {
		return
	}
	catalog.snapshotMu.RLock()
	providerRaw := append(json.RawMessage(nil), catalog.snapshot[providerID]...)
	catalog.snapshotMu.RUnlock()
	provider, ok := decodeObject(providerRaw)
	if !ok {
		return
	}
	models, ok := decodeObject(provider["models"])
	if !ok {
		return
	}
	modelRaw := models[model]
	modelObject, ok := decodeObject(modelRaw)
	if !ok {
		return
	}
	fillModel(modelRaw, entry)
	if entry.API == "" {
		modelProvider, _ := decodeObject(modelObject["provider"])
		npm, _ := jsonString(modelProvider["npm"])
		if npm == "" {
			npm, _ = jsonString(provider["npm"])
		}
		entry.API = dialectForNPM(npm)
	}
}

func fillModel(raw json.RawMessage, entry *Entry) {
	model, ok := decodeObject(raw)
	if !ok {
		return
	}
	if value, ok := jsonString(model["api"]); ok && entry.API == "" {
		entry.API = canonicalAPI(value)
	}

	if cost, ok := decodeObject(model["cost"]); ok {
		if entry.CostInput < 0 {
			entry.CostInput = memberRate(cost, "input")
		}
		if entry.CostOutput < 0 {
			entry.CostOutput = memberRate(cost, "output")
		}
		if entry.CostCacheRead < 0 {
			entry.CostCacheRead = memberRate(cost, "cache_read")
		}
		if entry.CostCacheWrite < 0 {
			entry.CostCacheWrite = memberRate(cost, "cache_write")
		}
		if entry.CostCacheWrite1H < 0 {
			entry.CostCacheWrite1H = memberRate(cost, "cache_write_1h")
		}
		fillTiers(entry, cost["tiers"])
	}
	if limit, ok := decodeObject(model["limit"]); ok {
		if entry.ContextWindow <= 0 {
			entry.ContextWindow = memberTokens(limit, "context")
		}
		if entry.MaxOutput <= 0 {
			entry.MaxOutput = memberTokens(limit, "output")
		}
	}
	fillEfforts(entry, model)
	if !entry.InterleavedDeclared {
		entry.InterleavedField, entry.InterleavedDeclared = canonicalInterleaved(model["interleaved"])
	}
	if entry.ImageInput == SupportUnknown {
		if modalities, ok := decodeObject(model["modalities"]); ok && isArray(modalities["input"]) {
			entry.ImageInput = SupportNo
			var inputs []json.RawMessage
			if json.Unmarshal(modalities["input"], &inputs) == nil {
				for _, raw := range inputs {
					if value, ok := jsonString(raw); ok && value == "image" {
						entry.ImageInput = SupportYes
					}
				}
			}
		}
	}
}

func fillTiers(entry *Entry, raw json.RawMessage) {
	if entry.TiersDeclared || !isArray(raw) {
		return
	}
	entry.TiersDeclared = true
	var tiers []json.RawMessage
	if json.Unmarshal(raw, &tiers) != nil {
		return
	}
	for _, rawTier := range tiers {
		if len(entry.Tiers) >= MaxTiers {
			break
		}
		tier, ok := decodeObject(rawTier)
		if !ok {
			continue
		}
		selector, ok := decodeObject(tier["tier"])
		if !ok {
			continue
		}
		typeName, _ := jsonString(selector["type"])
		if typeName != "context" {
			continue
		}
		threshold := memberTokens(selector, "size")
		if threshold <= 0 {
			continue
		}
		entry.Tiers = append(entry.Tiers, Tier{
			ContextThreshold: threshold, CostInput: memberRate(tier, "input"),
			CostOutput: memberRate(tier, "output"), CostCacheRead: memberRate(tier, "cache_read"),
			CostCacheWrite: memberRate(tier, "cache_write"), CostCacheWrite1H: memberRate(tier, "cache_write_1h"),
		})
	}
}

func fillEfforts(entry *Entry, model map[string]json.RawMessage) {
	if entry.EffortsKnown {
		return
	}
	if bytes.Equal(bytes.TrimSpace(model["reasoning"]), []byte("false")) {
		entry.EffortsKnown = true
		entry.Efforts = []string{}
		return
	}
	var options []json.RawMessage
	if !isArray(model["reasoning_options"]) || json.Unmarshal(model["reasoning_options"], &options) != nil || len(options) == 0 {
		return
	}
	for _, rawOption := range options {
		option, ok := decodeObject(rawOption)
		if !ok {
			continue
		}
		typeName, _ := jsonString(option["type"])
		if typeName != "effort" || !isArray(option["values"]) {
			continue
		}
		var values []json.RawMessage
		if json.Unmarshal(option["values"], &values) != nil {
			continue
		}
		for _, rawValue := range values {
			value, ok := jsonString(rawValue)
			if ok && value != "" && !contains(entry.Efforts, value) {
				entry.Efforts = append(entry.Efforts, value)
			}
		}
	}
	entry.EffortsKnown = true
}

func canonicalInterleaved(raw json.RawMessage) (string, bool) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("false")) {
		return "", true
	}
	field, ok := jsonString(raw)
	if !ok {
		object, valid := decodeObject(raw)
		if !valid {
			return "", false
		}
		field, ok = jsonString(object["field"])
		if !ok {
			return "", false
		}
	}
	switch strings.ToLower(field) {
	case "reasoning":
		return "reasoning", true
	case "reasoning_content":
		return "reasoning_content", true
	default:
		return "", false
	}
}

func canonicalAPI(value string) string {
	switch strings.ToLower(value) {
	case "openai-completions", "openai-responses", "anthropic-messages":
		return strings.ToLower(value)
	default:
		return "unsupported"
	}
}

func dialectForNPM(npm string) string {
	switch npm {
	case "@ai-sdk/openai-compatible":
		return "openai-completions"
	case "@ai-sdk/openai":
		return "openai-responses"
	case "@ai-sdk/anthropic":
		return "anthropic-messages"
	default:
		if npm == "" {
			return ""
		}
		return "unsupported"
	}
}

func (entry Entry) hasMetadata() bool {
	return entry.CostInput >= 0 || entry.CostOutput >= 0 || entry.CostCacheRead >= 0 ||
		entry.CostCacheWrite >= 0 || entry.CostCacheWrite1H >= 0 || entry.ContextWindow > 0 ||
		entry.MaxOutput > 0 || entry.ImageInput != SupportUnknown || entry.TiersDeclared ||
		entry.EffortsKnown || entry.API != "" || entry.InterleavedDeclared
}

type Split struct {
	CostInput           float64
	CostCacheRead       float64
	CostCacheWrite      float64
	CostOutput          float64
	UncachedInputTokens int64
}

type rates struct {
	input, output, cacheRead, cacheWrite, cacheWrite1H float64
}

func ratesForInput(entry Entry, inputTokens int64) rates {
	current := rates{entry.CostInput, entry.CostOutput, entry.CostCacheRead, entry.CostCacheWrite, entry.CostCacheWrite1H}
	var threshold int64 = -1
	for _, tier := range entry.Tiers {
		if tier.ContextThreshold <= 0 || inputTokens <= tier.ContextThreshold || tier.ContextThreshold <= threshold {
			continue
		}
		threshold = tier.ContextThreshold
		current.input = chooseRate(tier.CostInput, entry.CostInput)
		current.output = chooseRate(tier.CostOutput, entry.CostOutput)
		current.cacheRead = chooseRate(tier.CostCacheRead, entry.CostCacheRead)
		current.cacheWrite = chooseRate(tier.CostCacheWrite, entry.CostCacheWrite)
		current.cacheWrite1H = chooseRate(tier.CostCacheWrite1H, entry.CostCacheWrite1H)
	}
	return current
}

func chooseRate(value, fallback float64) float64 {
	if value >= 0 {
		return value
	}
	return fallback
}

// CacheWriteReplacesInput reports whether cache-write tokens replace ordinary input billing.
func CacheWriteReplacesInput(entry Entry) bool {
	return entry.CostInput < 0 || entry.CostCacheWrite < 0 || entry.CostCacheWrite >= entry.CostInput
}

// Price estimates one request in USD. Cache counts are subsets of input; the one-hour count is a
// subset of cache writes. Unknown cache rates use the input rate, and unknown one-hour rates use
// twice input. A context tier replaces the base rates for the whole request.
func Price(entry Entry, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens, cacheWrite1HTokens int64, split *Split) float64 {
	if split != nil {
		*split = Split{}
	}
	priceRates := ratesForInput(entry, inputTokens)
	if priceRates.input < 0 || priceRates.output < 0 {
		return -1
	}
	inputTokens = nonnegative(inputTokens)
	outputTokens = nonnegative(outputTokens)
	cacheReadTokens = nonnegative(cacheReadTokens)
	cacheWriteTokens = nonnegative(cacheWriteTokens)
	cacheWrite1HTokens = nonnegative(cacheWrite1HTokens)
	if cacheWrite1HTokens > cacheWriteTokens {
		cacheWrite1HTokens = cacheWriteTokens
	}
	if priceRates.cacheRead < 0 {
		priceRates.cacheRead = priceRates.input
	}
	writeReplacesInput := priceRates.cacheWrite < 0 || priceRates.cacheWrite >= priceRates.input
	if priceRates.cacheWrite < 0 {
		priceRates.cacheWrite = priceRates.input
	}
	if priceRates.cacheWrite1H < 0 {
		priceRates.cacheWrite1H = 2 * priceRates.input
	}
	uncached := inputTokens
	if cacheReadTokens >= uncached {
		uncached = 0
	} else {
		uncached -= cacheReadTokens
	}
	if writeReplacesInput {
		if cacheWriteTokens >= uncached {
			uncached = 0
		} else {
			uncached -= cacheWriteTokens
		}
	}
	inputCost := float64(uncached) * priceRates.input / 1e6
	readCost := float64(cacheReadTokens) * priceRates.cacheRead / 1e6
	writeCost := (float64(cacheWriteTokens-cacheWrite1HTokens)*priceRates.cacheWrite + float64(cacheWrite1HTokens)*priceRates.cacheWrite1H) / 1e6
	outputCost := float64(outputTokens) * priceRates.output / 1e6
	if split != nil {
		*split = Split{CostInput: inputCost, CostCacheRead: readCost, CostCacheWrite: writeCost, CostOutput: outputCost, UncachedInputTokens: uncached}
	}
	return inputCost + readCost + writeCost + outputCost
}

func nonnegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func memberRate(object map[string]json.RawMessage, name string) float64 {
	raw := bytes.TrimSpace(object[name])
	if len(raw) == 0 {
		return -1
	}
	var value float64
	if raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) != nil || strings.TrimSpace(text) == "" {
			return -1
		}
		parsed, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
		if err != nil {
			return -1
		}
		value = parsed
	} else {
		if raw[0] != '-' && (raw[0] < '0' || raw[0] > '9') || json.Unmarshal(raw, &value) != nil {
			return -1
		}
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return -1
	}
	return value
}

func memberTokens(object map[string]json.RawMessage, name string) int64 {
	raw := bytes.TrimSpace(object[name])
	if len(raw) == 0 {
		return 0
	}
	if raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return 0
		}
		return parseTokenCount(text)
	}
	var value int64
	if json.Unmarshal(raw, &value) != nil || value <= 0 {
		return 0
	}
	return value
}

func parseTokenCount(text string) int64 {
	value := strings.Trim(text, " \t")
	if value == "" {
		return 0
	}
	index := 0
	if value[index] == '+' || value[index] == '-' {
		index++
	}
	digitStart := index
	for index < len(value) && value[index] >= '0' && value[index] <= '9' {
		index++
	}
	if index == digitStart {
		return 0
	}
	number, err := strconv.ParseInt(value[:index], 10, 64)
	if err != nil || number <= 0 {
		return 0
	}
	rest := strings.Trim(value[index:], " \t")
	multiplier := int64(1)
	if len(rest) > 0 {
		switch rest[0] {
		case 'k', 'K':
			multiplier = 1000
			rest = rest[1:]
		case 'm', 'M':
			multiplier = 1000000
			rest = rest[1:]
		}
	}
	if strings.Trim(rest, " \t") != "" || number > math.MaxInt64/multiplier {
		return 0
	}
	return number * multiplier
}

func decodeObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(trimmed, &object) != nil || object == nil {
		return nil, false
	}
	return object, true
}

func jsonString(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", false
	}
	var value string
	if json.Unmarshal(trimmed, &value) != nil {
		return "", false
	}
	return value, true
}

func isArray(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '['
}

func contains(values []string, value string) bool {
	for _, existing := range values {
		if existing == value {
			return true
		}
	}
	return false
}

func cloneRawObject(object map[string]json.RawMessage) map[string]json.RawMessage {
	clone := make(map[string]json.RawMessage, len(object))
	for key, raw := range object {
		clone[key] = append(json.RawMessage(nil), raw...)
	}
	return clone
}

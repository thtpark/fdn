package domain

import (
	"slices"
	"strings"
)

const (
	LocalModelInitTTL int64 = 3600
	Qwen38Family            = "qwen3.8:27b"

	qwen38Q4MinMemoryBytes   int64 = 22 * (1 << 30)
	qwen38Q8MinMemoryBytes   int64 = 36 * (1 << 30)
	qwen38BF16MinMemoryBytes int64 = 64 * (1 << 30)
)

type LocalModelInitRequest struct {
	Command
	Model         string `json:"model"`
	Quantized     bool   `json:"quantized,omitempty"`
	Quantization  string `json:"quantization,omitempty"`
	SetPreference bool   `json:"set_preference,omitempty"`
	Reason        string `json:"reason"`
}

type LocalModelSpec struct {
	Family        string `json:"family"`
	Model         string `json:"model,omitempty"`
	Quantized     bool   `json:"quantized,omitempty"`
	Quantization  string `json:"quantization"`
	SetPreference bool   `json:"set_preference,omitempty"`
}

type LocalModelObservation struct {
	Provider          string `json:"provider"`
	RuntimeVersion    string `json:"runtime_version,omitempty"`
	Family            string `json:"family"`
	Model             string `json:"model,omitempty"`
	Quantization      string `json:"quantization,omitempty"`
	MemorySource      string `json:"memory_source,omitempty"`
	MemoryBytes       int64  `json:"memory_bytes,omitempty"`
	RequiredBytes     int64  `json:"required_bytes,omitempty"`
	GPUCount          int    `json:"gpu_count,omitempty"`
	CatalogPublished  bool   `json:"catalog_published,omitempty"`
	PreferenceApplied bool   `json:"preference_applied,omitempty"`
}

func AgentOperationTTLForKind(kind string) int64 {
	if kind == "local-model-init" {
		return LocalModelInitTTL
	}
	return AgentOperationTTL
}

func NormalizeLocalQuantization(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return "", nil
	case "auto":
		return "auto", nil
	case "none", "bf16":
		return "bf16", nil
	case "q8_0", "q8":
		return "q8_0", nil
	case "q4_k_m", "q4":
		return "q4_K_M", nil
	default:
		return "", Fail(Invalid, "quantization must be auto, q4_K_M, q8_0, or bf16")
	}
}

func ParseLocalModelSpec(model string, quantized bool, quantization string, setPreference bool) (LocalModelSpec, error) {
	raw := strings.ToLower(strings.TrimSpace(model))
	family := ""
	suffixQuant := ""
	switch raw {
	case "qwen3.8", "qwen-3.8", "qwen3.8:27b", "qwen-3.8-27b", "qwen3.8-27b", "qwen 3.8", "qwen 3.8 27b":
		family = Qwen38Family
	case "qwen3.8:27b-bf16":
		family, suffixQuant = Qwen38Family, "bf16"
	case "qwen3.8:27b-q8_0":
		family, suffixQuant = Qwen38Family, "q8_0"
	case "qwen3.8:27b-q4_k_m":
		family, suffixQuant = Qwen38Family, "q4_K_M"
	default:
		return LocalModelSpec{}, Fail(Invalid, "only Qwen 3.8 27B (qwen3.8:27b) is currently supported for local model initialization")
	}
	normalizedQuant, err := NormalizeLocalQuantization(quantization)
	if err != nil {
		return LocalModelSpec{}, err
	}
	if suffixQuant != "" && normalizedQuant != "" && normalizedQuant != suffixQuant && !(normalizedQuant == "auto" && suffixQuant != "bf16") {
		return LocalModelSpec{}, Fail(Invalid, "explicit model variant conflicts with the requested quantization")
	}
	if quantized && (normalizedQuant == "bf16" || suffixQuant == "bf16") {
		return LocalModelSpec{}, Fail(Invalid, "quantized initialization conflicts with unquantized bf16 weights")
	}
	resolvedQuant := normalizedQuant
	if suffixQuant != "" {
		resolvedQuant = suffixQuant
	} else if resolvedQuant == "" {
		if quantized {
			resolvedQuant = "auto"
		} else {
			resolvedQuant = "bf16"
		}
	}
	spec := LocalModelSpec{
		Family:        family,
		Quantized:     resolvedQuant != "bf16",
		Quantization:  resolvedQuant,
		SetPreference: setPreference,
	}
	if resolvedQuant != "auto" {
		spec.Model = family + "-" + resolvedQuant
	}
	return spec, ValidateLocalModelSpec(spec)
}

func ValidateLocalModelSpec(spec LocalModelSpec) error {
	if spec.Family != Qwen38Family {
		return Fail(Invalid, "unsupported local model family")
	}
	if !slices.Contains([]string{"auto", "q4_K_M", "q8_0", "bf16"}, spec.Quantization) {
		return Fail(Invalid, "unsupported local model quantization")
	}
	if spec.Quantized != (spec.Quantization != "bf16") {
		return Fail(Invalid, "local model quantization flags are inconsistent")
	}
	if spec.Quantization == "auto" {
		if spec.Model != "" {
			return Fail(Invalid, "automatic quantization resolves its concrete model variant on the target computer")
		}
		return nil
	}
	if spec.Model != spec.Family+"-"+spec.Quantization || !ValidModelID(spec.Model) {
		return Fail(Invalid, "local model tag does not match its family and quantization")
	}
	return nil
}

func ValidateLocalModelInitRequest(req LocalModelInitRequest) (LocalModelSpec, error) {
	if err := validateAgentOperationRequest(req.Command, req.Reason); err != nil {
		return LocalModelSpec{}, err
	}
	return ParseLocalModelSpec(req.Model, req.Quantized, req.Quantization, req.SetPreference)
}

func LocalModelMinMemoryBytes(quantization string) (int64, error) {
	switch quantization {
	case "q4_K_M":
		return qwen38Q4MinMemoryBytes, nil
	case "q8_0":
		return qwen38Q8MinMemoryBytes, nil
	case "bf16":
		return qwen38BF16MinMemoryBytes, nil
	default:
		return 0, Fail(Invalid, "concrete local model quantization is required")
	}
}

func SelectLocalModelForMemory(spec LocalModelSpec, memoryBytes int64) (model, quantization string, requiredBytes int64, err error) {
	if err := ValidateLocalModelSpec(spec); err != nil {
		return "", "", 0, err
	}
	if memoryBytes <= 0 {
		return "", "", 0, Fail(Prerequisite, "hardware memory capacity is unavailable for local model sizing")
	}
	if spec.Quantization == "auto" {
		switch {
		case memoryBytes >= qwen38Q8MinMemoryBytes:
			return spec.Family + "-q8_0", "q8_0", qwen38Q8MinMemoryBytes, nil
		case memoryBytes >= qwen38Q4MinMemoryBytes:
			return spec.Family + "-q4_K_M", "q4_K_M", qwen38Q4MinMemoryBytes, nil
		default:
			return "", "q4_K_M", qwen38Q4MinMemoryBytes, Fail(Prerequisite, "available memory does not meet the minimum 22 GiB requirement for quantized Qwen 3.8 27B")
		}
	}
	required, err := LocalModelMinMemoryBytes(spec.Quantization)
	if err != nil {
		return "", "", 0, err
	}
	model = spec.Family + "-" + spec.Quantization
	if memoryBytes < required {
		return model, spec.Quantization, required, Fail(Prerequisite, "available memory does not fit the requested Qwen 3.8 27B weights and KV cache; enable quantization or select a smaller variant")
	}
	return model, spec.Quantization, required, nil
}

func ValidateLocalModelObservation(obs LocalModelObservation) error {
	if obs.Provider != "local" || obs.Family != Qwen38Family {
		return Fail(Invalid, "local model observation requires the local provider and supported model family")
	}
	if obs.RuntimeVersion != "" && !ValidHealthVersion(obs.RuntimeVersion) {
		return Fail(Invalid, "local model runtime version is malformed")
	}
	if obs.Quantization != "" && !slices.Contains([]string{"q4_K_M", "q8_0", "bf16"}, obs.Quantization) {
		return Fail(Invalid, "local model observation requires a concrete quantization")
	}
	if obs.Model != "" && (!ValidModelID(obs.Model) || (obs.Quantization != "" && obs.Model != obs.Family+"-"+obs.Quantization)) {
		return Fail(Invalid, "local model observation model tag does not match its quantization")
	}
	if obs.MemorySource != "" && !slices.Contains([]string{"nvidia-vram", "system-ram"}, obs.MemorySource) {
		return Fail(Invalid, "local model memory source must be nvidia-vram or system-ram")
	}
	if obs.MemoryBytes < 0 || obs.RequiredBytes < 0 || obs.GPUCount < 0 || obs.GPUCount > 64 {
		return Fail(Invalid, "local model resource counts must be non-negative and bounded")
	}
	return nil
}

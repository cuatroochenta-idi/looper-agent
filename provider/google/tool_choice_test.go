package google

import (
	"testing"

	"google.golang.org/genai"

	"github.com/cuatroochenta-idi/looper-agent/provider"
)

func TestGoogleToolChoice_Auto(t *testing.T) {
	cfg := buildToolConfig(provider.ToolChoiceAuto())
	if cfg == nil || cfg.FunctionCallingConfig.Mode != genai.FunctionCallingConfigModeAuto {
		t.Errorf("auto should map to Mode=AUTO, got %+v", cfg)
	}
}

func TestGoogleToolChoice_Required_MapsToAny(t *testing.T) {
	cfg := buildToolConfig(provider.ToolChoiceRequired())
	if cfg == nil || cfg.FunctionCallingConfig.Mode != genai.FunctionCallingConfigModeAny {
		t.Errorf("required should map to Mode=ANY, got %+v", cfg)
	}
	if len(cfg.FunctionCallingConfig.AllowedFunctionNames) != 0 {
		t.Errorf("required (any tool) must not restrict AllowedFunctionNames, got %v",
			cfg.FunctionCallingConfig.AllowedFunctionNames)
	}
}

func TestGoogleToolChoice_None(t *testing.T) {
	cfg := buildToolConfig(provider.ToolChoiceNone())
	if cfg == nil || cfg.FunctionCallingConfig.Mode != genai.FunctionCallingConfigModeNone {
		t.Errorf("none should map to Mode=NONE, got %+v", cfg)
	}
}

func TestGoogleToolChoice_Specific_RestrictsAllowedNames(t *testing.T) {
	cfg := buildToolConfig(provider.ToolChoiceSpecific("publish_pages"))
	if cfg == nil || cfg.FunctionCallingConfig.Mode != genai.FunctionCallingConfigModeAny {
		t.Errorf("specific should set Mode=ANY (Gemini has no exact-tool mode), got %+v", cfg)
	}
	names := cfg.FunctionCallingConfig.AllowedFunctionNames
	if len(names) != 1 || names[0] != "publish_pages" {
		t.Errorf("specific should populate AllowedFunctionNames=[publish_pages], got %v", names)
	}
}

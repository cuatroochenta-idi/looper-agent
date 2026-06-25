package looper

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/loop"
	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/skill"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// lazyStubProvider satisfies provider.LLMProvider so MustNewAgent can build an
// agent. These tests never call Run, so the chat methods are inert.
type lazyStubProvider struct{}

func (lazyStubProvider) Model() string                   { return "stub" }
func (lazyStubProvider) Translator() provider.Translator { return nil }
func (lazyStubProvider) Chat(_ context.Context, _ provider.LLMRequest) (*provider.LLMResponse, error) {
	return &provider.LLMResponse{}, nil
}
func (lazyStubProvider) ChatStream(_ context.Context, _ provider.LLMRequest) (<-chan provider.StreamChunk, error) {
	ch := make(chan provider.StreamChunk)
	close(ch)
	return ch, nil
}

// ─── Fakes (unified Skill API: Name/Title/Summary/RegisterTools/PromptFragment) ──

// eagerSkill is a plain Skill: its tools and full prompt fragment are always
// present in the agent.
type eagerSkill struct{}

func (eagerSkill) Name() string    { return "calculator" }
func (eagerSkill) Title() string   { return "Calculator" }
func (eagerSkill) Summary() string { return "Do arithmetic." }
func (eagerSkill) RegisterTools(reg *tool.ToolRegistry) {
	reg.Add(tool.MustNewTool(struct{}{},
		func(_ context.Context, _ struct{}) (string, error) { return "ok", nil },
		tool.ToolConfig{Name: "add", Description: "Add numbers."},
	))
}
func (eagerSkill) PromptFragment() string { return "EAGER_FRAGMENT: you can add numbers." }

// lazySkill embeds skill.Lazy, so it is a LazySkill: only Title()+Summary()
// surface until load_skill activates it, at which point its tools + the full
// PromptFragment become available.
type lazySkill struct {
	skill.Lazy
}

func (lazySkill) Name() string    { return "translator" }
func (lazySkill) Title() string   { return "Translator" }
func (lazySkill) Summary() string { return "Translate text into Catalan." }
func (lazySkill) RegisterTools(reg *tool.ToolRegistry) {
	reg.Add(tool.MustNewTool(struct{}{},
		func(_ context.Context, _ struct{}) (string, error) { return "translated", nil },
		tool.ToolConfig{Name: "translate", Description: "Translate text."},
	))
	reg.Add(tool.MustNewTool(struct{}{},
		func(_ context.Context, _ struct{}) (string, error) { return "detected", nil },
		tool.ToolConfig{Name: "detect_lang", Description: "Detect the language."},
	))
}
func (lazySkill) PromptFragment() string {
	return "LAZY_FRAGMENT: always translate into Catalan before answering."
}

// ─── Test helpers ────────────────────────────────────────────────────────────

// toolNameSet collects the names of the given tools for set comparison.
func toolNameSet(tools []*tool.Tool) map[string]bool {
	set := make(map[string]bool, len(tools))
	for _, t := range tools {
		set[t.Name()] = true
	}
	return set
}

// loadSkillCall builds an assistant tool-call invoking load_skill{skill: name},
// exactly as the loop records a real model call.
func loadSkillCall(name string) message.ToolCall {
	args, _ := json.Marshal(loadSkillInput{Skill: name})
	return message.ToolCall{ID: "call-1", Name: loadSkillToolName, Arguments: args}
}

// ─── (a) LazySkill does NOT announce its tools until loaded ──────────────────

func TestLazySkill_NotAnnouncedUntilLoaded(t *testing.T) {
	lz := lazySkill{}
	base := []*tool.Tool{} // no eager/standalone tools beyond load_skill in this slice
	all := allToolsFor(t, base, lz)
	gate := buildLazyGating(base, lazyToolsFor(t, lz), []skill.LazySkill{lz}, all)

	h := message.NewHistory()
	h.AddUserMessage("hola")

	got := toolNameSet(gate(context.Background(), h))
	if got["translate"] || got["detect_lang"] {
		t.Fatalf("lazy tools must be hidden before load_skill; got %v", got)
	}
}

// ─── (b) After load_skill{X}: activeLazySkills returns X and gating includes its tools ──

func TestLoadSkill_ActivatesToolsAfterCall(t *testing.T) {
	lz := lazySkill{}
	lazies := []skill.LazySkill{lz}

	h := message.NewHistory()
	h.AddUserMessage("translate this")
	h.AddAssistantMessage("", []message.ToolCall{loadSkillCall("translator")})
	h.AddToolResult("call-1", loadSkillToolName, "loaded", false)

	active := activeLazySkills(h, lazies)
	if len(active) != 1 || active[0].Name() != "translator" {
		t.Fatalf("activeLazySkills = %v, want [translator]", lazySkillNames(active))
	}

	base := []*tool.Tool{}
	all := allToolsFor(t, base, lz)
	gate := buildLazyGating(base, lazyToolsFor(t, lz), lazies, all)

	got := toolNameSet(gate(context.Background(), h))
	if !got["translate"] || !got["detect_lang"] {
		t.Fatalf("lazy tools must be exposed after load_skill; got %v", got)
	}
}

// ─── (c) load_skill with invalid name → error listing valid skills ───────────

func TestLoadSkill_InvalidName(t *testing.T) {
	lz := lazySkill{}
	loadTool := newLoadSkillTool([]skill.LazySkill{lz})

	args, _ := json.Marshal(loadSkillInput{Skill: "does_not_exist"})
	_, err := loadTool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected error for unknown skill, got nil")
	}
	if !strings.Contains(err.Error(), "does_not_exist") {
		t.Errorf("error should name the bad skill; got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "translator") {
		t.Errorf("error should list valid skill names; got %q", err.Error())
	}
}

// ─── (d) load_skill valid → returns FULL PromptFragment + "Unlocked tools:" ──

func TestLoadSkill_ValidReturnsFragmentAndTools(t *testing.T) {
	lz := lazySkill{}
	loadTool := newLoadSkillTool([]skill.LazySkill{lz})

	args, _ := json.Marshal(loadSkillInput{Skill: "translator"})
	out, err := loadTool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, lz.PromptFragment()) {
		t.Errorf("result must include full PromptFragment; got %q", out)
	}
	if !strings.Contains(out, "Unlocked tools:") {
		t.Errorf("result must include 'Unlocked tools:'; got %q", out)
	}
	if !strings.Contains(out, "translate") || !strings.Contains(out, "detect_lang") {
		t.Errorf("result must list unlocked tool names; got %q", out)
	}
}

// ─── (e) Eager Skill is intact (tools + fragment always present) ─────────────

func TestEagerSkill_AlwaysPresent(t *testing.T) {
	agent := MustNewAgent(lazyStubProvider{}, "base", eagerSkill{}, lazySkill{})

	// Eager tool is in the registry.
	if !toolNameSet(agent.tools)["add"] {
		t.Errorf("eager tool 'add' must be registered; got %v", toolNameSet(agent.tools))
	}

	// The eager skill is recorded in agent.skills, whose PromptFragment is
	// concatenated into the loop's system prompt (agent.go). The lazy skill is
	// NOT in agent.skills — its fragment is delivered via load_skill, not the
	// base prompt — so its fragment never leaks into the eager concatenation.
	var eagerFragments string
	for _, s := range agent.skills {
		if s.Name() == "translator" {
			t.Errorf("lazy skill must NOT be registered as an eager skill")
		}
		eagerFragments += s.PromptFragment()
	}
	if !strings.Contains(eagerFragments, "EAGER_FRAGMENT") {
		t.Errorf("eager PromptFragment must be among the eager skills; got %q", eagerFragments)
	}
	if strings.Contains(eagerFragments, "LAZY_FRAGMENT") {
		t.Errorf("lazy PromptFragment must NOT be in the eager prompt; got %q", eagerFragments)
	}

	// The lazy index (appended to the base prompt) shows the lazy skill but
	// never its fragment.
	idx := lazySkillsIndex([]skill.LazySkill{lazySkill{}})
	if strings.Contains(idx, "LAZY_FRAGMENT") {
		t.Errorf("lazy index must NOT contain the lazy PromptFragment; got %q", idx)
	}

	// Eager tool is always exposed by gating, even with no history.
	got := toolNameSet(agent.dynamicTools(context.Background(), message.NewHistory()))
	if !got["add"] {
		t.Errorf("eager tool must always be exposed by gating; got %v", got)
	}
	if !got[loadSkillToolName] {
		t.Errorf("load_skill must always be exposed by gating; got %v", got)
	}
}

// ─── (f) User's WithDynamicTools wins over auto-gating ───────────────────────

func TestWithDynamicTools_UserWins(t *testing.T) {
	sentinel := tool.MustNewTool(struct{}{},
		func(_ context.Context, _ struct{}) (string, error) { return "x", nil },
		tool.ToolConfig{Name: "sentinel_only", Description: "marker"},
	)
	userFn := func(_ context.Context, _ *message.History) []*tool.Tool {
		return []*tool.Tool{sentinel}
	}

	agent := MustNewAgent(lazyStubProvider{}, "base",
		lazySkill{},
		WithDynamicTools(loop.DynamicToolsFunc(userFn)),
	)

	got := toolNameSet(agent.dynamicTools(context.Background(), message.NewHistory()))
	if len(got) != 1 || !got["sentinel_only"] {
		t.Fatalf("user dynamicTools must win, got %v", got)
	}
}

// ─── (g) lazySkillsIndex shows Title+Summary, NEVER the PromptFragment ───────

func TestLazySkillsIndex_TitleSummaryOnly(t *testing.T) {
	lz := lazySkill{}
	idx := lazySkillsIndex([]skill.LazySkill{lz})

	if !strings.Contains(idx, lz.Title()) {
		t.Errorf("index must contain Title; got %q", idx)
	}
	if !strings.Contains(idx, lz.Summary()) {
		t.Errorf("index must contain Summary; got %q", idx)
	}
	if !strings.Contains(idx, `load_skill skill="translator"`) {
		t.Errorf("index must show the load_skill invocation; got %q", idx)
	}
	if strings.Contains(idx, lz.PromptFragment()) {
		t.Errorf("index must NEVER contain the full PromptFragment; got %q", idx)
	}

	// No lazy skills → empty index.
	if got := lazySkillsIndex(nil); got != "" {
		t.Errorf("lazySkillsIndex(nil) = %q, want empty", got)
	}
}

// ─── shared test fixtures ────────────────────────────────────────────────────

// lazyToolsFor builds the name→tools map the gating function expects, mirroring
// what NewAgent does when it registers a lazy skill's tools in isolation.
func lazyToolsFor(t *testing.T, lz skill.LazySkill) map[string][]*tool.Tool {
	t.Helper()
	reg := tool.NewToolRegistry()
	lz.RegisterTools(reg)
	return map[string][]*tool.Tool{lz.Name(): reg.Tools()}
}

// allToolsFor builds the full ordered tool slice (base ∪ lazy tools ∪ load_skill)
// that gating filters against, mirroring NewAgent's registry order.
func allToolsFor(t *testing.T, base []*tool.Tool, lz skill.LazySkill) []*tool.Tool {
	t.Helper()
	reg := tool.NewToolRegistry()
	for _, b := range base {
		reg.Add(b)
	}
	lz.RegisterTools(reg)
	reg.Add(newLoadSkillTool([]skill.LazySkill{lz}))
	return reg.Tools()
}

package looper

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cuatroochenta-idi/looper-agent/loop"
	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/skill"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// loadSkillToolName is the name of the auto-injected tool the model calls to
// activate a lazy skill.
const loadSkillToolName = "load_skill"

// loadSkillInput is the argument schema for the load_skill tool.
type loadSkillInput struct {
	Skill string `json:"skill" jsonschema:"description=Name of the skill to load,required"`
}

// newLoadSkillTool builds the auto-injected load_skill tool over the given lazy
// skills. Calling it validates the requested skill name against the lazy set
// and returns the skill's PromptFragment plus the list of unlocked tools. The
// result lands in history (as a tool result), which is how the model receives
// the fragment — that is why it is deliberately NOT placed in the system
// prompt.
func newLoadSkillTool(lazy []skill.LazySkill) *tool.Tool {
	byName := make(map[string]skill.LazySkill, len(lazy))
	for _, lz := range lazy {
		byName[lz.Name()] = lz
	}

	handler := func(_ context.Context, in loadSkillInput) (string, error) {
		lz, ok := byName[in.Skill]
		if !ok {
			return "", fmt.Errorf(
				"unknown skill %q; valid skills: %s",
				in.Skill, strings.Join(lazySkillNames(lazy), ", "),
			)
		}

		tmp := tool.NewToolRegistry()
		lz.RegisterTools(tmp)
		toolNames := toolNamesOf(tmp.Tools())

		var b strings.Builder
		b.WriteString(lz.PromptFragment())
		b.WriteString("\n\nUnlocked tools: ")
		b.WriteString(strings.Join(toolNames, ", "))
		return b.String(), nil
	}

	return tool.MustNewTool(loadSkillInput{}, handler, tool.ToolConfig{
		Name: loadSkillToolName,
		Description: "Load a skill by name to unlock its tools and instructions. " +
			"Skills available to load are listed under the \"Skills\" section of " +
			"the system prompt. Pass the exact skill name.",
	})
}

// activeLazySkills scans the history for successful load_skill calls and
// returns the lazy skills that have been activated. Activation is detected
// structurally from assistant tool-calls (never from text markers), so it is
// robust to model prose. nil-safe: a nil history yields no active skills.
func activeLazySkills(h *message.History, lazy []skill.LazySkill) []skill.LazySkill {
	if h == nil || len(lazy) == 0 {
		return nil
	}

	byName := make(map[string]skill.LazySkill, len(lazy))
	for _, lz := range lazy {
		byName[lz.Name()] = lz
	}

	seen := make(map[string]bool)
	var active []skill.LazySkill
	for _, msg := range h.Messages() {
		if msg.Type != message.MessageAssistant {
			continue
		}
		for _, tc := range msg.ToolCalls {
			if tc.Name != loadSkillToolName {
				continue
			}
			var in loadSkillInput
			if err := json.Unmarshal(tc.Arguments, &in); err != nil {
				continue
			}
			lz, ok := byName[in.Skill]
			if !ok || seen[in.Skill] {
				continue
			}
			seen[in.Skill] = true
			active = append(active, lz)
		}
	}
	return active
}

// lazySkillsIndex renders the load-on-demand skills index appended to the
// system prompt. Returns "" when there are no lazy skills.
func lazySkillsIndex(lazy []skill.LazySkill) string {
	if len(lazy) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Skills (load on demand with the load_skill tool)\n")
	for _, lz := range lazy {
		b.WriteString("- ")
		b.WriteString(lz.Title())
		b.WriteString(` (load_skill skill="`)
		b.WriteString(lz.Name())
		b.WriteString(`") — `)
		b.WriteString(lz.Summary())
		b.WriteString("\n")
	}
	return b.String()
}

// buildLazyGating returns a DynamicToolsFunc that, on every turn, exposes the
// base tools (eager + standalone + load_skill) plus the tools of any lazy skill
// activated so far. The returned slice preserves the order of `all` (the full
// registry order) so the provider's prompt-cache prefix stays stable across
// turns. Defensive: when history is nil only the base tools are exposed.
func buildLazyGating(
	base []*tool.Tool,
	lazyTools map[string][]*tool.Tool,
	lazy []skill.LazySkill,
	all []*tool.Tool,
) loop.DynamicToolsFunc {
	baseSet := make(map[string]bool, len(base))
	for _, t := range base {
		baseSet[t.Name()] = true
	}

	return func(_ context.Context, h *message.History) []*tool.Tool {
		allowed := make(map[string]bool, len(baseSet))
		for name := range baseSet {
			allowed[name] = true
		}
		for _, lz := range activeLazySkills(h, lazy) {
			for _, t := range lazyTools[lz.Name()] {
				allowed[t.Name()] = true
			}
		}

		out := make([]*tool.Tool, 0, len(allowed))
		for _, t := range all {
			if allowed[t.Name()] {
				out = append(out, t)
			}
		}
		return out
	}
}

// lazySkillNames returns the names of the given lazy skills, in order.
func lazySkillNames(lazy []skill.LazySkill) []string {
	names := make([]string, len(lazy))
	for i, lz := range lazy {
		names[i] = lz.Name()
	}
	return names
}

// toolNamesOf returns the names of the given tools, in order.
func toolNamesOf(tools []*tool.Tool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name()
	}
	return names
}

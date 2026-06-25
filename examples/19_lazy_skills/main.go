// Example: lazy skills loaded on demand with the auto-injected load_skill tool.
//
// A Skill groups tools + a prompt fragment under the unified skill API
// (Name/Title/Summary/RegisterTools/PromptFragment). There are two flavours:
//
//   - eager Skill  — its tools and full PromptFragment are always present in
//     the system prompt and tool list from turn one.
//   - LazySkill    — same API, plus an embedded skill.Lazy marker. Until the
//     model loads it, only its Title + Summary appear in a compact "Skills"
//     index in the system prompt; its tools stay hidden and its full
//     PromptFragment is withheld. The model activates it by calling the
//     auto-injected load_skill tool with the skill's Name. The tool result
//     carries the full PromptFragment + the list of unlocked tools, and from
//     that turn on the skill's tools are exposed.
//
// This keeps the base prompt small: heavy, situational instructions only enter
// the context window when the model decides the skill is relevant.
//
// Here we wire an eager Calculator skill and a lazy Translator skill. The model
// sees `add` immediately, but only sees `translate` after it calls
// load_skill skill="translator".
//
// Usage:
//
//	export OPENAI_API_KEY=sk-...
//	go run examples/19_lazy_skills/main.go
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/cuatroochenta-idi/looper-agent/looper"
	"github.com/cuatroochenta-idi/looper-agent/provider/openai"
	"github.com/cuatroochenta-idi/looper-agent/skill"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// ─── Eager skill: calculator (always available) ──────────────────────────────

type AddInput struct {
	A float64 `json:"a" jsonschema:"description=First operand,required"`
	B float64 `json:"b" jsonschema:"description=Second operand,required"`
}

type CalculatorSkill struct{}

func (CalculatorSkill) Name() string  { return "calculator" }
func (CalculatorSkill) Title() string { return "Calculator" }
func (CalculatorSkill) Summary() string {
	return "Add numbers."
}

func (CalculatorSkill) RegisterTools(reg *tool.ToolRegistry) {
	reg.Add(tool.MustNewTool(AddInput{},
		func(_ context.Context, in AddInput) (string, error) {
			return fmt.Sprintf("%g", in.A+in.B), nil
		},
		tool.ToolConfig{Name: "add", Description: "Add two numbers."},
	))
}

func (CalculatorSkill) PromptFragment() string {
	return "\nYou can do arithmetic with the add tool."
}

// ─── Lazy skill: translator (loaded on demand) ───────────────────────────────

type TranslateInput struct {
	Text string `json:"text" jsonschema:"description=Text to translate,required"`
}

// TranslatorSkill embeds skill.Lazy, so it is a LazySkill: its tool + full
// PromptFragment stay out of the base context until the model calls
// load_skill skill="translator".
type TranslatorSkill struct {
	skill.Lazy
	TargetLang string
}

func (s TranslatorSkill) Name() string  { return "translator" }
func (s TranslatorSkill) Title() string { return "Translator" }
func (s TranslatorSkill) Summary() string {
	return fmt.Sprintf("Translate text into %s.", s.TargetLang)
}

func (s TranslatorSkill) RegisterTools(reg *tool.ToolRegistry) {
	target := strings.ToLower(s.TargetLang)
	reg.Add(tool.MustNewTool(TranslateInput{},
		func(_ context.Context, in TranslateInput) (string, error) {
			// Toy "translation" — a real version would call an API.
			return fmt.Sprintf("[%s] %s", target, in.Text), nil
		},
		tool.ToolConfig{Name: "translate", Description: "Translate text into the configured language."},
	))
}

func (s TranslatorSkill) PromptFragment() string {
	return fmt.Sprintf(
		"\nYou are a translator. Use the translate tool to render text into %s, "+
			"then present the translation to the user.",
		s.TargetLang,
	)
}

// ─── Wire-up ───────────────────────────────────────────────────────────────────

func main() {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "OPENAI_API_KEY required")
		os.Exit(1)
	}

	agent := looper.MustNewAgent(openai.NewProvider(key),
		"You are a helpful assistant. Some skills must be loaded with the "+
			"load_skill tool before their tools become available — load a skill "+
			"when the task calls for it.",
		CalculatorSkill{},                      // eager: tools + fragment from turn one
		TranslatorSkill{TargetLang: "Catalan"}, // lazy: appears in the Skills index only
	)

	res, err := agent.Run(context.Background(),
		"Translate 'good morning' into Catalan.")
	if err != nil {
		fmt.Fprintf(os.Stderr, "run failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("──── Output ────")
	fmt.Println(res.Output)
	fmt.Println("────────────────")
	fmt.Printf("turns: %d  status: %s  cost: $%.6f\n", res.Turns, res.Status, res.Cost.TotalUSD)
}

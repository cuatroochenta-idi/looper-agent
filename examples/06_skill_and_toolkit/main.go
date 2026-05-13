// Example: composing an agent from a Toolkit and a Skill.
//
//   - Toolkit  — groups related tools that share internal state. Does NOT
//                modify the system prompt.
//   - Skill    — like a toolkit but ALSO appends a prompt fragment, giving the
//                LLM thematic instructions for the new capability.
//
// Here we build a `CalculatorToolkit` (math tools) and a `TranslatorSkill`
// (translation tool + a "respond in <lang>" instruction).
//
// Usage:
//
//	set -a && source .env.local && set +a
//	go run examples/06_skill_and_toolkit/main.go
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/cuatroochenta-idi/looper-agent/looper"
	"github.com/cuatroochenta-idi/looper-agent/provider/openai"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// ─── Toolkit: math operations ──────────────────────────────────────────────────

type AddInput struct {
	A float64 `json:"a" jsonschema:"description=First operand"`
	B float64 `json:"b" jsonschema:"description=Second operand"`
}

type MulInput struct {
	A float64 `json:"a" jsonschema:"description=First operand"`
	B float64 `json:"b" jsonschema:"description=Second operand"`
}

type CalculatorToolkit struct{}

func (CalculatorToolkit) RegisterTools(reg *tool.ToolRegistry) {
	reg.Add(tool.MustNewTool(AddInput{},
		func(_ context.Context, in AddInput) (string, error) {
			return fmt.Sprintf("%g", in.A+in.B), nil
		},
		tool.ToolConfig{
			Name:        "add",
			Description: "Add two numbers and return the sum.",
			Parallel:    true,
		},
	))
	reg.Add(tool.MustNewTool(MulInput{},
		func(_ context.Context, in MulInput) (string, error) {
			return fmt.Sprintf("%g", in.A*in.B), nil
		},
		tool.ToolConfig{
			Name:        "multiply",
			Description: "Multiply two numbers and return the product.",
			Parallel:    true,
		},
	))
}

// ─── Skill: translator with prompt fragment ────────────────────────────────────

type TranslateInput struct {
	Text string `json:"text" jsonschema:"description=Text to translate"`
}

type TranslatorSkill struct {
	TargetLang string
}

func (s TranslatorSkill) Name() string { return "translator" }

func (s TranslatorSkill) RegisterTools(reg *tool.ToolRegistry) {
	target := strings.ToLower(s.TargetLang)
	reg.Add(tool.MustNewTool(TranslateInput{},
		func(_ context.Context, in TranslateInput) (string, error) {
			// Toy "translation" — real version would call an API.
			return fmt.Sprintf("[%s] %s", target, in.Text), nil
		},
		tool.ToolConfig{
			Name:        "translate",
			Description: "Translate text into the configured target language.",
		},
	))
}

func (s TranslatorSkill) PromptFragment() string {
	return fmt.Sprintf(
		"\nYou ALWAYS speak %s. If the user writes in another language, translate "+
			"their message via the translate tool before answering.",
		s.TargetLang,
	)
}

// ─── Wire-up ───────────────────────────────────────────────────────────────────

func main() {
	ctx := context.Background()

	p := openai.NewProvider(os.Getenv("OPENAI_API_KEY"))

	agent := looper.MustNewAgent(p,
		"You are a helpful assistant.",
		CalculatorToolkit{},                       // toolkit (no prompt fragment)
		TranslatorSkill{TargetLang: "Catalan"},    // skill (adds a prompt fragment)
	)

	result, err := agent.Run(ctx,
		"Compute (3 + 4) * 5 and explain the steps.")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Output: %s\n", result.Output)
	fmt.Printf("Cost:   $%.6f  Turns: %d\n", result.Cost.TotalUSD, result.Turns)
}

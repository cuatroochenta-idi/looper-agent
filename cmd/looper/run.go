package main

import (
	"fmt"
	"log"
)

func runCmd(args []string) {
	if len(args) == 0 {
		log.Fatal("Usage: looper run <main.go path> [--args '...']")
	}
	mainPath := args[0]

	fmt.Printf("Running %s in debug mode...\n", mainPath)
	fmt.Println("(Debug runner not yet implemented)")

	// Placeholder: execute the Go project with debug flags
	// cmd := exec.Command("go", append([]string{"run", mainPath}, extraArgs...)...)
	// cmd.Env = append(os.Environ(),
	//     "LOOPER_DEBUG=true",
	//     "LOOPER_OTEL_VERBOSE=true",
	// )
	// cmd.Stdout = os.Stdout
	// cmd.Stderr = os.Stderr
	// cmd.Run()
}

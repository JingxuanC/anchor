package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/JingxuanC/anchor/internal/adapter"
)

const maxCaptureInput = 1 << 20

func cmdAdapter(args []string) {
	if len(args) < 1 {
		adapterUsage()
		os.Exit(2)
	}
	switch args[0] {
	case "capabilities":
		adapterCapabilities(args[1:])
	case "render":
		adapterRender(args[1:])
	case "capture":
		adapterCapture(args[1:])
	default:
		adapterUsage()
		os.Exit(2)
	}
}

func adapterUsage() {
	fmt.Fprintln(os.Stderr, `  anchor adapter capabilities [--json]
  anchor adapter render --adapter ID (--target DIR | --print)
  anchor adapter capture --adapter ID (--event FILE | --stdin) (--out FILE | --print)`)
}

func adapterCapabilities(args []string) {
	fs := flag.NewFlagSet("adapter capabilities", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "JSON output")
	_ = fs.Parse(args)
	all := adapter.All()
	if *asJSON {
		b, _ := json.MarshalIndent(map[string]any{"adapters": all, "evidence_grades": map[string]string{"D": "documentation reviewed", "V": "verified in this environment", "S": "supported end-to-end"}, "verified_native_hooks": false}, "", "  ")
		fmt.Println(string(b))
		return
	}
	for _, a := range all {
		fmt.Printf("%-12s grade=%s native_hook=%s e2e=%v\n", a.ID, a.EvidenceGrade, a.NativeHookStatus, a.EndToEndVerified)
	}
}

func adapterRender(args []string) {
	fs := flag.NewFlagSet("adapter render", flag.ExitOnError)
	id := fs.String("adapter", "", "adapter id")
	target := fs.String("target", "", "explicit output directory")
	printOut := fs.Bool("print", false, "print dry-run output; write nothing")
	_ = fs.Parse(args)
	if *id == "" {
		die(fmt.Errorf("--adapter is required"))
	}
	if *target == "" && !*printOut {
		die(fmt.Errorf("--target or --print is required; no default config path is used"))
	}
	if *printOut {
		out, files, err := adapter.Render(*id)
		if err != nil {
			die(err)
		}
		contents := []map[string]any{}
		for _, f := range files {
			contents = append(contents, map[string]any{"path": f.Path, "content": string(f.Content)})
		}
		b, _ := json.MarshalIndent(map[string]any{"rendered": out, "files": contents, "dry_run": true}, "", "  ")
		fmt.Println(string(b))
		return
	}
	out, paths, err := adapter.Write(*id, *target)
	if err != nil {
		die(err)
	}
	b, _ := json.MarshalIndent(map[string]any{"rendered": out, "paths": paths, "dry_run": true, "installed": false}, "", "  ")
	fmt.Println(string(b))
}

func adapterCapture(args []string) {
	fs := flag.NewFlagSet("adapter capture", flag.ExitOnError)
	id := fs.String("adapter", "", "adapter id")
	eventFile := fs.String("event", "", "hook event JSON file")
	stdin := fs.Bool("stdin", false, "read hook event JSON from stdin")
	outFile := fs.String("out", "", "explicit output file")
	printOut := fs.Bool("print", false, "print normalized capture")
	_ = fs.Parse(args)
	if *id == "" {
		die(fmt.Errorf("--adapter is required"))
	}
	if (*eventFile == "") == !*stdin {
		die(fmt.Errorf("exactly one of --event or --stdin is required"))
	}
	if (*outFile == "") == !*printOut {
		die(fmt.Errorf("exactly one of --out or --print is required"))
	}
	var input []byte
	var err error
	if *stdin {
		input, err = io.ReadAll(io.LimitReader(os.Stdin, maxCaptureInput+1))
	} else {
		input, err = os.ReadFile(*eventFile)
	}
	if err != nil {
		die(err)
	}
	if len(input) > maxCaptureInput {
		die(fmt.Errorf("capture input exceeds %d bytes", maxCaptureInput))
	}
	result, err := adapter.Capture(*id, input)
	if err != nil {
		die(err)
	}
	b, _ := json.MarshalIndent(result, "", "  ")
	b = append(b, '\n')
	if *printOut {
		_, _ = os.Stdout.Write(b)
		return
	}
	if strings.TrimSpace(*outFile) == "" {
		die(fmt.Errorf("--out is required"))
	}
	f, err := os.OpenFile(*outFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		die(err)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		die(err)
	}
	if err := f.Close(); err != nil {
		die(err)
	}
	fmt.Printf("capture metadata written to %s (no content captured)\n", *outFile)
}

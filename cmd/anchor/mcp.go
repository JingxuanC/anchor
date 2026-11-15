package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/JingxuanC/anchor/internal/mcp"
)

// cmdMcp runs the read-only MCP stdio server so agents can inspect the Anchor
// workspace directly (tools: anchor_status, anchor_tree,
// anchor_evidence_verify, anchor_restore_plan).
func cmdMcp(args []string) {
	if len(args) < 1 || args[0] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: anchor mcp serve [--dir DIR]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("mcp serve", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	_ = fs.Parse(args[1:])
	store := openStore(*dir)
	ctx, cancel := signal.NotifyContext(context.Background(), wrapSignals()...)
	defer cancel()
	if err := mcp.Serve(ctx, store.Dir, os.Stdin, os.Stdout); err != nil && !errors.Is(err, context.Canceled) {
		die(err)
	}
}

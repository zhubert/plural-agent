package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/zhubert/erg/internal/dashboard"
)

var dashboardPort int

var dashboardCmd = &cobra.Command{
	Use:     "dashboard",
	Short:   "Open a live web dashboard for monitoring agents",
	GroupID: "daemon",
	Long: `Starts a local HTTP server that serves a real-time web dashboard
showing all running erg daemons, their work items, and session logs.

Uses Server-Sent Events for live updates.

Examples:
  erg dashboard              # Start on default port 21122
  erg dashboard --port 8080  # Start on custom port`,
	RunE: runDashboard,
}

func init() {
	dashboardCmd.Flags().IntVar(&dashboardPort, "port", 21122, "Port for the dashboard server")
	rootCmd.AddCommand(dashboardCmd)
}

func runDashboard(cmd *cobra.Command, args []string) error {
	addr := fmt.Sprintf("localhost:%d", dashboardPort)
	fmt.Printf("erg dashboard → http://%s\n", addr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	srv := dashboard.New(addr)
	return srv.Run(ctx)
}

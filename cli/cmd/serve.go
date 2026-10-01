package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"heall/internal/server"
)

func newServeCmd() *cobra.Command {
	var (
		runsDir, webDir string
		port            int
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the dashboard and the runs it shows",
		Long: `Serve the dashboard on this machine.

Every "heall run" records its events under .heall/<run id>. This command
serves those recordings to the dashboard, following a run that is still in
progress, so a run started in another terminal appears live. Runs need no
extra flag to be watchable.

The dashboard itself is the static build in web/out ("make web"). Without it,
only the API is served, which is enough for the dev server ("pnpm dev" in
web/).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := filepath.Abs(runsDir)
			if err != nil {
				return err
			}
			if webDir == "" {
				webDir = server.FindWebDir()
			}
			srv := &server.Server{RunsDir: dir, WebDir: webDir}
			// Local only: the event stream holds source code and test output.
			ln, err := net.Listen("tcp", server.Addr(port))
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "heall dashboard: http://localhost:%d\n", port)
			fmt.Fprintf(out, "  runs from %s\n", dir)
			if webDir == "" {
				fmt.Fprintln(out, "  the dashboard is not built (run \"make web\"); serving the API only")
			}
			fmt.Fprintln(out, "  press Ctrl-C to stop")

			httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
			go func() {
				<-cmd.Context().Done()
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = httpSrv.Shutdown(ctx)
				_ = httpSrv.Close()
			}()
			if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&runsDir, "runs", ".heall", "directory that holds the recorded runs")
	cmd.Flags().StringVar(&webDir, "web", "", "directory of the built dashboard (default: web/out)")
	cmd.Flags().IntVar(&port, "port", 7777, "port to listen on, on this machine only")
	return cmd
}

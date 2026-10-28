// watchzit-agent is the WatchZit agent: it runs as a service on a
// monitored computer and reports CPU, memory, disks, network traffic and
// services to the WatchZit server over one outbound connection.
//
//	watchzit-agent enroll --code WZ1-…   # join this computer (the console shows the code)
//	watchzit-agent run                   # in the foreground (the service runs this)
//	watchzit-agent install [--code WZ1-…] # register and start the service
//	watchzit-agent uninstall
//	watchzit-agent status | sample | version
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/max-zit/watchzit/internal/agent"
)

var version = "dev"

func main() {
	agent.Version = version
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() error {
	fmt.Fprintln(os.Stderr, `usage: watchzit-agent <command> [options]

commands:
  enroll     join this computer to a WatchZit server: --code WZ1-…
             [--force] [--if-not-enrolled] [--pending-ok] [--dir DIR]
  forget     remove the enrollment (configuration and key)
  run        run the agent in the foreground
  install    install and start the service (Linux: --code WZ1-… enrolls too)
  uninstall  stop and remove the service
  status     show the enrollment
  sample     print one sample and the inventory of this computer as JSON
  version    print the version`)
	return errors.New("no command")
}

func run(args []string) error {
	if isService() {
		return runService(agent.DefaultDir())
	}
	if len(args) == 0 {
		return usage()
	}
	cmd, args := args[0], args[1:]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	dir := fs.String("dir", agent.DefaultDir(), "data directory")
	code := fs.String("code", "", "enrollment code from the console (WZ1-…)")
	force := fs.Bool("force", false, "enroll again even if already enrolled")
	ifNot := fs.Bool("if-not-enrolled", false, "do nothing when already enrolled (installer upgrades)")
	pendingOK := fs.Bool("pending-ok", false, "if the server cannot be reached, let the service finish the enrollment later")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch cmd {
	case "version":
		fmt.Println("watchzit-agent", version)
		return nil
	case "enroll":
		d, err := agent.ParseEnrollCode(*code)
		if err != nil {
			return err
		}
		if c, err := agent.LoadConfig(*dir); err == nil && !*force {
			if *ifNot {
				fmt.Printf("Already enrolled with %s as %q; nothing to do.\n", c.ServerURL, c.Device)
				return nil
			}
			return fmt.Errorf("already enrolled with %s as %q; use --force to enroll again", c.ServerURL, c.Device)
		}
		if *force {
			// a new enrollment starts with a new key
			_ = agent.Forget(*dir)
		}
		c, err := agent.Enroll(ctx, *dir, d)
		var refused *agent.RefusedError
		if err != nil && *pendingOK && !errors.As(err, &refused) {
			// No network yet (installed at boot, server down): the service finishes it.
			if perr := agent.SavePending(*dir, d); perr != nil {
				return perr
			}
			fmt.Println("The server cannot be reached now; the agent service will finish the enrollment:", err)
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Printf("Enrolled as device %q (#%d).\n", c.Device, c.DeviceID)
		return nil
	case "forget":
		if err := agent.Forget(*dir); err != nil {
			return err
		}
		fmt.Println("Enrollment removed.")
		return nil
	case "status":
		c, err := agent.LoadConfig(*dir)
		if err != nil {
			return err
		}
		fmt.Printf("server:      %s\nfingerprint: %s\ndevice:      %s (#%d)\nagent id:    %s\nenrolled:    %s\n",
			c.ServerURL, c.Fingerprint, c.Device, c.DeviceID, c.AgentID, c.EnrolledAt.Local().Format(time.DateTime))
		return nil
	case "sample":
		s := agent.NewSampler()
		s.Sample(ctx, nil)
		time.Sleep(time.Second)
		e := json.NewEncoder(os.Stdout)
		e.SetIndent("", "  ")
		if err := e.Encode(s.Sample(ctx, nil)); err != nil {
			return err
		}
		return e.Encode(agent.CollectInventory(ctx))
	case "run":
		log := slog.New(slog.NewTextHandler(io.MultiWriter(os.Stderr, agent.OpenLog(*dir)), nil))
		return runAgent(ctx, *dir, log)
	case "install":
		return installService(*code)
	case "uninstall":
		return uninstallService()
	}
	return usage()
}

func runAgent(ctx context.Context, dir string, log *slog.Logger) error {
	// Wait for the enrollment: the MSI enrolls before it starts the service,
	// or leaves a pending enrollment when the server could not be reached.
	var cfg *agent.Config
	wait := 15 * time.Second
	for {
		c, err := agent.LoadConfig(dir)
		if err == nil {
			cfg = c
			break
		}
		if !errors.Is(err, agent.ErrNotEnrolled) {
			return err
		}
		if d, ok := agent.LoadPending(dir); ok {
			c, err := agent.Enroll(ctx, dir, d)
			if err == nil {
				log.Info("pending enrollment finished", "device", c.Device)
				cfg = c
				break
			}
			log.Warn("pending enrollment failed; trying again later", "server", d.Server, "err", err)
		} else {
			log.Warn("not enrolled yet; waiting", "dir", dir)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		wait = min(2*wait, 15*time.Minute)
	}
	log.Info("WatchZit agent starting", "version", version, "server", cfg.ServerURL, "device", cfg.Device)
	a := &agent.Agent{Dir: dir, Cfg: cfg, Log: log}
	return a.Run(ctx)
}

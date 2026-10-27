// watchzit-server is the WatchZit server: it polls the devices, stores the
// measurements and serves the management console.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"

	"github.com/max-zit/watchzit/internal/auth"
	"github.com/max-zit/watchzit/internal/logfile"
	"github.com/max-zit/watchzit/internal/netacl"
	"github.com/max-zit/watchzit/internal/poller"
	"github.com/max-zit/watchzit/internal/store"
	"github.com/max-zit/watchzit/internal/tlsutil"
	"github.com/max-zit/watchzit/internal/tsdb"
	"github.com/max-zit/watchzit/internal/web"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func defaultDataDir() string {
	if runtime.GOOS == "windows" {
		if p := os.Getenv("ProgramData"); p != "" {
			return filepath.Join(p, "WatchZit")
		}
	}
	return "/var/lib/watchzit"
}

func run() error {
	dataDir := flag.String("data-dir", env("WATCHZIT_DATA", defaultDataDir()), "data directory (certificates, secret key, logs)")
	dbURL := flag.String("db", os.Getenv("WATCHZIT_DB"), "PostgreSQL URL, e.g. postgres://watchzit:…@localhost/watchzit")
	devDB := flag.Bool("dev-embedded-db", false, "start a private PostgreSQL in the data directory (development and evaluation only)")
	mgmtListen := flag.String("mgmt-listen", "", "console address; overrides the saved setting (e.g. 127.0.0.1:8443)")
	devHTTP := flag.String("dev-http", "", "also serve the console over plain HTTP on this loopback address (development only)")
	devNo2FA := flag.Bool("dev-no-2fa", false, "sign in with the password only (development only, needs --dev-embedded-db; shown as a warning and audited)")
	resetACL := flag.Bool("reset-allowlist", false, "allow the console again from the private networks (10/8, 172.16/12, 192.168/16, fc00::/7) and exit")
	unblock := flag.String("unblock-ip", "", "lift the sign-in block of an address (or \"all\") and exit")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("watchzit-server", version)
		return nil
	}
	if *devNo2FA && !*devDB {
		return errors.New("--dev-no-2fa works only together with --dev-embedded-db")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		return err
	}
	var logOut io.Writer = os.Stdout
	if lw, err := logfile.Open(filepath.Join(*dataDir, "logs", "server.log"), 0o600); err == nil {
		defer lw.Close()
		logOut = io.MultiWriter(os.Stdout, lw)
	}
	log := slog.New(slog.NewTextHandler(logOut, nil))

	if *devDB {
		pg, url, err := startEmbeddedDB(*dataDir)
		if err != nil {
			return fmt.Errorf("embedded database: %w", err)
		}
		defer pg.Stop()
		*dbURL = url
		log.Warn("using the embedded development database", "dir", filepath.Join(*dataDir, "pgdata"))
	}
	if *dbURL == "" {
		return errors.New("database URL required (--db or WATCHZIT_DB)")
	}
	st, err := store.Open(ctx, *dbURL)
	if err != nil {
		return err
	}
	defer st.Close()
	key, created, err := store.LoadOrCreateSecretKey(*dataDir)
	if err != nil {
		return fmt.Errorf("secret key: %w", err)
	}
	if err := st.UseSecretKey(key); err != nil {
		return err
	}
	if created {
		log.Warn("created a new secret key; back it up together with the database", "file", filepath.Join(*dataDir, store.SecretKeyFile))
	}

	switch {
	case *resetACL:
		if err := web.ResetMgmtAllowList(ctx, st); err != nil {
			return err
		}
		_ = st.Audit(ctx, "-", "local", "settings.network.reset", "settings", "console allow-list reset to the private networks from the command line")
		fmt.Println("The console allows the private networks again. Restart the service if it is running.")
		return nil
	case *unblock != "":
		n, err := st.Unblock(ctx, *unblock)
		if err != nil {
			return err
		}
		_ = st.Audit(ctx, "-", "local", "login.unblock", "ip:"+*unblock, "sign-in block lifted from the command line")
		fmt.Printf("Lifted %d block(s).\n", n)
		return nil
	}

	db := tsdb.New(st.Pool(), log)
	pl := poller.New(st, db, log)
	pl.OnChange = func(cs []store.StatusChange) {
		for _, c := range cs {
			log.Info("check status changed", "check", c.CheckID, "device", c.DeviceID, "from", c.From, "to", c.To, "result", c.Text)
		}
	}
	srv, err := web.New(ctx, st, db, pl, log)
	if err != nil {
		return err
	}
	srv.Version = version
	if *devNo2FA {
		srv.DevNo2FA = true
		log.Warn("TEST MODE: two-factor sign-in is switched off (--dev-no-2fa); never use this on a production server")
		_ = st.Audit(ctx, "-", "local", "settings.2fa.off", "server", "server started with --dev-no-2fa: sign-in with the password only")
	}
	net0, err := srv.Network(ctx)
	if err != nil {
		return err
	}
	if *mgmtListen != "" {
		net0.MgmtListen = *mgmtListen
	}
	if err := net0.Validate(); err != nil {
		return err
	}
	srv.RunningMgmt = net0.MgmtListen
	srv.ListenOverride = *mgmtListen != ""

	consoleCert, err := tlsutil.LoadOrCreate(filepath.Join(*dataDir, "tls"), "console", "WatchZit console", 5)
	if err != nil {
		return fmt.Errorf("console certificate: %w", err)
	}

	if n, err := st.UserCount(ctx); err != nil {
		return err
	} else if n == 0 {
		tok := auth.RandomToken(18)
		p := filepath.Join(*dataDir, "setup-token.txt")
		if err := os.WriteFile(p, []byte(tok+"\n"), 0o600); err != nil {
			return err
		}
		srv.SetSetupToken(tok)
		srv.SetupTokenFile = p
		log.Warn("no users yet: open the console and finish the setup; the setup token is in the file", "file", p)
	}

	errc := make(chan error, 2)
	var servers []*http.Server
	raw, err := net.Listen("tcp", net0.MgmtListen)
	if err != nil {
		return fmt.Errorf("console listener %s: %w", net0.MgmtListen, err)
	}
	cfg := tlsutil.ServerConfig(consoleCert)
	l := tls.NewListener(&netacl.Listener{Listener: raw, List: srv.MgmtACL, OnRefuse: func(remote string) {
		log.Debug("connection refused by allow-list", "remote", remote)
	}}, cfg)
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 60 * time.Second, WriteTimeout: 120 * time.Second,
		IdleTimeout: 120 * time.Second, MaxHeaderBytes: 64 << 10, ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelDebug)}
	servers = append(servers, hs)
	go func() { errc <- hs.Serve(l) }()
	log.Info("listening", "listener", "console", "addr", raw.Addr().String())
	if *devHTTP != "" {
		a, ok := netacl.AddrOf(*devHTTP)
		if !ok || !a.IsLoopback() {
			return errors.New("--dev-http must be a loopback address such as 127.0.0.1:8080")
		}
		dh := &http.Server{Addr: *devHTTP, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
		servers = append(servers, dh)
		go func() { errc <- dh.ListenAndServe() }()
		log.Warn("development HTTP console (no TLS)", "addr", *devHTTP)
	}
	log.Info("WatchZit server started", "version", version)

	done := make(chan struct{}, 2)
	go func() { db.Run(ctx); done <- struct{}{} }()
	go func() { pl.Run(ctx); done <- struct{}{} }()
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = st.PurgeSessions(ctx)
			}
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	stop()
	shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, h := range servers {
		_ = h.Shutdown(shut)
	}
	<-done
	<-done
	log.Info("stopped")
	return nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// startEmbeddedDB runs PostgreSQL from the data directory on a fixed
// loopback port (development only).
func startEmbeddedDB(dir string) (*embeddedpostgres.EmbeddedPostgres, string, error) {
	const port = 54339
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().Port(port).
		DataPath(filepath.Join(dir, "pgdata")).RuntimePath(filepath.Join(dir, "pgrt")).
		BinariesPath(filepath.Join(dir, "pgbin")).Database("watchzit").Logger(io.Discard))
	if err := pg.Start(); err != nil {
		return nil, "", err
	}
	return pg, fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/watchzit?sslmode=disable", port), nil
}

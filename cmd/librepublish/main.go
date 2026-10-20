// librepublish is the whole CMS in one file: the API, the admin, the
// database (SQLite) and the plugin host. Download it, run it.
//
//	librepublish serve [--data ./data] [--addr :8080] [--url https://cms.example.com]
//	librepublish user create --email you@example.com --name You --password - [--role Administrator]
//	librepublish token create --name PRODUCTION [--role "Production site"]
//	librepublish seed-blocks
//	librepublish import backup.tar.gz [--data ./data] [--dry-run] [--force] [--secret-key-file old-secret.key]
//	librepublish version
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/admin"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/cli"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/cms"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/datalock"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/restore"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/update"
)

// version is this build's: set from the release tag (make dist VERSION=…).
var version = "dev"

// usageError is a command line that's wrong, which exits 2.
type usageError struct{ error }

func usagef(format string, args ...any) error { return usageError{fmt.Errorf(format, args...)} }

// Exit codes besides 0, 1 and 2 (usage), from BSD's sysexits.
const (
	exitDataErr  = 65 // the backup can't be imported (EX_DATAERR)
	exitTempFail = 75 // the data directory is in use; try later (EX_TEMPFAIL)
)

func main() {
	// Where releases are asked for (GitHub's API unless set: a mirror, a test).
	if api := os.Getenv("LIBREPUBLISH_RELEASES_API"); api != "" {
		update.API = strings.TrimRight(api, "/")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, stop, os.Args[1:])
	stop()
	if err == nil && handOverTo != "" {
		// The update button: everything is closed; become the release.
		err = update.Exec(handOverTo)
	}
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return
	}
	if !errors.Is(err, cli.ErrFailed) { // a tool's failure is already printed
		fmt.Fprintln(os.Stderr, "librepublish:", err)
	}
	os.Exit(exitCode(err))
}

func exitCode(err error) int {
	var usage usageError
	switch {
	case errors.As(err, &usage):
		return 2
	case errors.Is(err, cli.ErrFailed):
		return 3
	case errors.Is(err, datalock.ErrLocked):
		return exitTempFail
	case errors.Is(err, restore.ErrInvalid), errors.Is(err, restore.ErrTooLarge), errors.Is(err, restore.ErrWrongKey):
		return exitDataErr
	}
	return 1
}

func run(ctx context.Context, stop func(), args []string) error {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		return serve(ctx, stop, args)
	case "user":
		return user(ctx, args)
	case "token":
		return token(ctx, args)
	case "seed-blocks":
		return seedBlocks(ctx, args)
	case "import":
		return importBackup(ctx, args)
	case "update":
		return selfUpdate(ctx, args)
	case "version", "--version":
		fmt.Println("librepublish", version)
		return nil
	case "help", "-h", "--help":
		usage(os.Stdout)
		return nil
	}
	// Working on a site: login and kin, or one of its tools by name.
	err := cli.Run(ctx, cli.Env{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Version: version}, cmd, args)
	if errors.Is(err, cli.ErrUnknown) {
		return usageError{fmt.Errorf("%w (librepublish help lists the commands)", err)}
	}
	return err
}

func usage(w io.Writer) {
	fmt.Fprint(w, `librepublish: a headless CMS in one file. It runs the site, and works on one.

Working on a site (from a terminal, a script, or an AI app):

  login        sign in to a site            https://cms.example.com [--name NAME] [--token TOKEN]
  TOOL         run one of the site's tools  e.g. list_pages --status draft, get_page about
  tools        what the site's tools are    [NAME] [--json]
  whoami       the site, you and your role
  doctor       whether all that works
  mcp          a local MCP server for AI apps; mcp setup says how to add it
  profiles, logout                          --profile NAME picks a site; --json prints answers raw

Running a site:

  serve        run it (the default)         --data DIR --addr :8080 --url https://cms.example.com
  user create  add a person                 --email --name --password [--role Administrator]
  user password      a new password         --email --password
  user disable-2fa   turn two-factor off    --email
  token create issue a service token        --name PRODUCTION [--role "Production site"]
  seed-blocks  install the starter block types
  import       replace the site with a backup  ARCHIVE-OR-DIR [--dry-run] [--force]
               (this core's, or the Rails core's)  [--secret-key-file F] [--json]
  update       install the newest release over this binary   [--check]
  version

update downloads the newest release for this machine, checks it against the
release's checksums.txt and puts it in place of this binary (the old one is
kept beside it as .old); restart the service after. --check only says
whether there's one. In a container, deploy the new image instead
(ghcr.io/martin-business-consultants/librepublishgo:<version>).

import reads a backup from Tools › Backup (either core), a Rails data
directory backup, or any of them unpacked. --dry-run says what it would do
and changes nothing; without it the site in --data is replaced (it's kept in
DATA_DIR/replaced-<time>/), which needs --force when it has users, and
serve mustn't be running on it. --secret-key-file is the old install's
secret.key (or a file holding its SECRET_KEY), to carry over the secrets it
sealed. Exit codes: 65 for a backup that can't be imported, 75 when the
data directory is in use. docs/import.md has the rest.

--password - reads the password from the first line of stdin, and with no
--password it comes from LIBREPUBLISH_PASSWORD: either keeps it out of the
process list and shell history.

Every flag has an environment variable: DATA_DIR, ADDR (or PORT), APP_URL,
SITE_KEY, CMS_DELIVERY_RATE_LIMIT (requests a minute per token, 0 for no
limit), SECRET_KEY, IMPORT_MAX_BYTES (the largest backup the admin may
upload, 10 GiB by default), and TRUST_PROXY=1 behind a proxy (Caddy, nginx, a load
balancer) so client addresses come from X-Forwarded-For. LOG_LEVEL (debug,
info, warn, error) and LOG_FORMAT (text, json) shape the log.
`)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// parse reads the flags every command shares, then the command's own, and
// the environment; a command takes no arguments besides flags.
func parse(name string, args []string, extra func(*flag.FlagSet)) (cms.Config, error) {
	cfg, rest, err := parseArgs(name, args, extra)
	if err == nil && len(rest) > 0 {
		return cms.Config{}, usagef("%s takes no arguments besides flags: %q", name, rest)
	}
	return cfg, err
}

// parseArgs is parse for a command that takes arguments too, before or
// after its flags.
func parseArgs(name string, args []string, extra func(*flag.FlagSet)) (cms.Config, []string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	addr := env("ADDR", "")
	if addr == "" {
		addr = ":" + env("PORT", "8080")
	}
	data := fs.String("data", env("DATA_DIR", "./data"), "where the database and files live")
	listen := fs.String("addr", addr, "address to listen on")
	url := fs.String("url", env("APP_URL", ""), "the CMS's own address, which asset URLs start with")
	if extra != nil {
		extra(fs)
	}
	var rest []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return cms.Config{}, nil, err
			}
			return cms.Config{}, nil, usageError{err}
		}
		if fs.NArg() == 0 {
			break
		}
		rest, args = append(rest, fs.Arg(0)), fs.Args()[1:]
	}
	// Unset, the CMS's address is Settings › General's (cms.App.BaseURL).
	base := strings.TrimRight(*url, "/")
	limit, err := rateLimit(os.Getenv("CMS_DELIVERY_RATE_LIMIT"))
	if err != nil {
		return cms.Config{}, nil, err
	}
	var importMax int64
	if v := os.Getenv("IMPORT_MAX_BYTES"); v != "" {
		if importMax, err = strconv.ParseInt(v, 10, 64); err != nil || importMax <= 0 {
			return cms.Config{}, nil, usagef("IMPORT_MAX_BYTES is a number of bytes, not %q", v)
		}
	}
	logger, err := newLogger(name)
	if err != nil {
		return cms.Config{}, nil, err
	}
	trust := os.Getenv("TRUST_PROXY") == "1" || os.Getenv("TRUST_PROXY") == "true"
	return cms.Config{DataDir: *data, Addr: *listen, BaseURL: base, SiteKey: os.Getenv("SITE_KEY"), SecretKey: os.Getenv("SECRET_KEY"),
		Version: version, UpdateCheck: os.Getenv("UPDATE_CHECK") != "0" && os.Getenv("UPDATE_CHECK") != "false", DeliveryRateLimit: limit, TrustProxy: trust, Logger: logger, ImportMaxBytes: importMax}, rest, nil
}

// rateLimit reads CMS_DELIVERY_RATE_LIMIT: unset is the default, 0 is none
// (Config's -1).
func rateLimit(v string) (int, error) {
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, usagef("CMS_DELIVERY_RATE_LIMIT is requests a minute (0 for no limit), not %q", v)
	}
	if n == 0 {
		return -1, nil
	}
	return n, nil
}

// newLogger is LOG_LEVEL and LOG_FORMAT's log, on stderr. serve logs
// from info up, the one-off commands warnings and errors.
func newLogger(cmd string) (*slog.Logger, error) {
	level := slog.LevelWarn
	if cmd == "serve" {
		level = slog.LevelInfo
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		if err := level.UnmarshalText([]byte(v)); err != nil {
			return nil, usagef("LOG_LEVEL is debug, info, warn or error, not %q", v)
		}
	}
	opts := &slog.HandlerOptions{Level: level}
	switch f := env("LOG_FORMAT", "text"); f {
	case "text":
		return slog.New(slog.NewTextHandler(os.Stderr, opts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(os.Stderr, opts)), nil
	default:
		return nil, usagef("LOG_FORMAT is text or json, not %q", f)
	}
}

// serve runs until a signal, then lets requests in flight finish (a second
// signal quits at once) before closing the app.
func serve(ctx context.Context, stop func(), args []string) (err error) {
	cfg, err := parse("serve", args, nil)
	if err != nil {
		return err
	}
	cfg.Admin = admin.Handler()
	if err := checkPersistent(cfg); err != nil {
		return err
	}
	// A release the update button put in this data directory, newer than
	// this binary, is what runs (update.Choose).
	if bin, ok := update.Choose(cfg.DataDir, version); ok {
		cfg.Logger.Info("handing over to the release in the data directory", "binary", bin, "this", version)
		if err := update.Exec(bin); err != nil {
			cfg.Logger.Warn("couldn't hand over; running this version", "err", err)
		}
	}
	handover := make(chan string, 1)
	cfg.HandOver = func(bin string) {
		select {
		case handover <- bin:
		default:
		}
	}
	// No import under a server (servers may overlap, as a deploy does).
	lock, err := datalock.AcquireShared(cfg.DataDir)
	switch {
	case errors.Is(err, datalock.ErrLocked):
		return fmt.Errorf("%w: is an import replacing %s?", err, cfg.DataDir)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("%w: the user librepublish runs as (uid %d) can't write to %s; give it that directory", err, os.Getuid(), cfg.DataDir)
	case err != nil:
		return err
	}
	defer func() { err = errors.Join(err, lock.Release()) }()
	// One site per data directory: a volume shared by mistake isn't.
	if err := datalock.ClaimForSite(cfg.DataDir, cfg.SiteKey); err != nil {
		return err
	}
	app, err := cms.NewServer(cfg)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, app.Close()) }()
	// Bound first, so a taken port says so at once; requests wait in the
	// backlog until the plugins are loaded.
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Addr)
	if err != nil {
		return err
	}
	if err := app.Start(ctx); err != nil {
		ln.Close()
		return err
	}
	srv := &http.Server{
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(cfg.Logger.Handler(), slog.LevelWarn),
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	cfg.Logger.Info("libre publish is up", "version", version, "admin", app.App().BaseURL()+"/", "data", cfg.DataDir, "addr", ln.Addr().String())
	// Serving: a release being tried has come up (update.Confirm).
	update.Confirm(cfg.DataDir, version)

	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	case bin := <-handover:
		handOverTo = bin
		cfg.Logger.Info("updating: finishing requests in flight, then handing over", "binary", bin)
	}
	stop()
	cfg.Logger.Info("shutting down: finishing requests in flight (a second signal quits now)")
	shut, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := srv.Shutdown(shut); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// password is --password, read from stdin when it's "-", or
// LIBREPUBLISH_PASSWORD when it wasn't given.
func password(flagValue string) (string, error) {
	switch flagValue {
	case "-":
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && (!errors.Is(err, io.EOF) || line == "") {
			return "", fmt.Errorf("reading the password from stdin: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	case "":
		return os.Getenv("LIBREPUBLISH_PASSWORD"), nil
	}
	return flagValue, nil
}

// required says which of the flags named weren't given.
func required(flags ...[2]string) error {
	for _, f := range flags {
		if f[1] == "" {
			return usagef("--%s is required", f[0])
		}
	}
	return nil
}

func user(ctx context.Context, args []string) error {
	if len(args) > 0 && (args[0] == "password" || args[0] == "disable-2fa") {
		return userAccount(ctx, args[0], args[1:])
	}
	if len(args) == 0 || args[0] != "create" {
		return usagef("usage: librepublish user create|password|disable-2fa --email … [--name … --password … --role Administrator]")
	}
	var email, name, pass, role string
	cfg, err := parse("user create", args[1:], func(fs *flag.FlagSet) {
		fs.StringVar(&email, "email", "", "their email, which they sign in with")
		fs.StringVar(&name, "name", "", "their name")
		fs.StringVar(&pass, "password", "", "at least 10 characters; - reads it from stdin, unset uses LIBREPUBLISH_PASSWORD")
		fs.StringVar(&role, "role", "Administrator", "the role")
	})
	if err != nil {
		return err
	}
	if pass, err = password(pass); err != nil {
		return err
	}
	if err := required([2]string{"email", email}, [2]string{"password", pass}); err != nil {
		return err
	}
	return withApp(cfg, func(app *cms.App) error {
		id, err := app.CreateUserWithRole(ctx, name, email, pass, role)
		if err != nil {
			return err
		}
		fmt.Printf("Created user %d (%s), %s. Sign in at %s/\n", id, email, role, app.BaseURL())
		return nil
	})
}

// userAccount: a new password for someone locked out, or a second factor
// turned off for an account the Rails core set it up on.
func userAccount(ctx context.Context, cmd string, args []string) error {
	var email, pass string
	cfg, err := parse("user "+cmd, args, func(fs *flag.FlagSet) {
		fs.StringVar(&email, "email", "", "whose account")
		if cmd == "password" {
			fs.StringVar(&pass, "password", "", "the new password, at least 10 characters; - reads it from stdin, unset uses LIBREPUBLISH_PASSWORD")
		}
	})
	if err != nil {
		return err
	}
	need := [][2]string{{"email", email}}
	if cmd == "password" {
		if pass, err = password(pass); err != nil {
			return err
		}
		need = append(need, [2]string{"password", pass})
	}
	if err := required(need...); err != nil {
		return err
	}
	return withApp(cfg, func(app *cms.App) error {
		if cmd == "password" {
			if err := app.SetPassword(ctx, email, pass); err != nil {
				return err
			}
			fmt.Println("Password changed; their sessions were signed out.")
			return nil
		}
		if err := app.DisableTwoFactor(ctx, email); err != nil {
			return err
		}
		fmt.Println("Two-factor sign-in is off for", email+". They sign in with their password alone now.")
		return nil
	})
}

func token(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "create" {
		return usagef(`usage: librepublish token create --name PRODUCTION [--role "Production site"]`)
	}
	var name, role string
	cfg, err := parse("token create", args[1:], func(fs *flag.FlagSet) {
		fs.StringVar(&name, "name", "PRODUCTION", "what it's for")
		fs.StringVar(&role, "role", "Production site", "the role it acts with")
	})
	if err != nil {
		return err
	}
	return withApp(cfg, func(app *cms.App) error {
		plain, err := app.CreateServiceTokenForRole(ctx, name, role)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Issued %q on %s. It is shown once; keep it in the site's environment, never in a repo.\n", name, role)
		fmt.Println(plain)
		return nil
	})
}

func seedBlocks(ctx context.Context, args []string) error {
	cfg, err := parse("seed-blocks", args, nil)
	if err != nil {
		return err
	}
	return withApp(cfg, func(app *cms.App) error {
		added, err := app.SeedBlockTypes(ctx, nil)
		if err != nil {
			return err
		}
		fmt.Printf("Installed %d block types: %s\n", len(added), strings.Join(added, ", "))
		return nil
	})
}

// withApp opens the install for a one-off command (nothing runs in the
// background) and closes it after.
func withApp(cfg cms.Config, fn func(*cms.App) error) (err error) {
	app, err := cms.NewApp(cfg)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, app.Close()) }()
	return fn(app)
}

// importBackup stages a backup, says what it holds, and (without
// --dry-run) replaces the site in --data with it. The report goes to
// stdout, as text or --json.
func importBackup(ctx context.Context, args []string) (err error) {
	var dryRun, force, asJSON bool
	var keyFile string
	cfg, rest, err := parseArgs("import", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&dryRun, "dry-run", false, "say what it would do, change nothing")
		fs.BoolVar(&force, "force", false, "replace a site that has users")
		fs.StringVar(&keyFile, "secret-key-file", "", "the old install's secret.key, or a file holding its SECRET_KEY (- reads stdin)")
		fs.BoolVar(&asJSON, "json", false, "print the report as JSON")
	})
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return usagef("usage: librepublish import ARCHIVE-OR-DIR [--data DIR] [--dry-run] [--force] [--secret-key-file F] [--json]")
	}
	opts := restore.Options{DataDir: cfg.DataDir, SecretKey: cfg.SecretKey, Force: force, Actor: "librepublish import"}
	if keyFile != "" {
		if opts.SourceKey, err = readKey(keyFile); err != nil {
			return err
		}
	}
	// Nothing may have the database open while it's swapped; a dry run
	// only stages, which a running server doesn't mind.
	if !dryRun {
		lock, lockErr := datalock.Acquire(cfg.DataDir)
		if lockErr != nil {
			return fmt.Errorf("%w: stop librepublish serve on %s first, or import from Tools › Backup in its admin", lockErr, cfg.DataDir)
		}
		defer func() { err = errors.Join(err, lock.Release()) }()
	}
	fmt.Fprintln(os.Stderr, "Reading", rest[0], "…")
	imp, err := restore.Stage(ctx, rest[0], opts)
	if err != nil {
		return err
	}
	defer func() {
		if dryRun || err != nil {
			err = errors.Join(err, imp.Discard())
		}
	}()
	rep := imp.Report
	if !dryRun {
		if rep, err = imp.Apply(ctx, opts); err != nil {
			if errors.Is(err, restore.ErrHasUsers) {
				printReport(imp.Report, asJSON)
				return fmt.Errorf("%w; add --force to replace it (the current site is kept in %s/replaced-<time>/)", err, cfg.DataDir)
			}
			return err
		}
	}
	printReport(rep, asJSON)
	if dryRun {
		fmt.Fprintln(os.Stderr, "Dry run: nothing was changed.")
	} else {
		fmt.Fprintln(os.Stderr, "Done. Everyone was signed out; start librepublish serve and sign in with an account from the imported site.")
	}
	return nil
}

func printReport(rep *restore.Report, asJSON bool) {
	if !asJSON {
		rep.WriteText(os.Stdout)
		return
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(rep) // stdout went away; nothing to do
}

// readKey is --secret-key-file's contents: a file, or stdin for "-".
func readKey(path string) (string, error) {
	var raw []byte
	var err error
	if path == "-" {
		raw, err = io.ReadAll(io.LimitReader(os.Stdin, 4096))
	} else {
		raw, err = os.ReadFile(path) //nolint:gosec // the operator's own key file
	}
	if err != nil {
		return "", fmt.Errorf("reading the secret key: %w", err)
	}
	key := strings.TrimSpace(string(raw))
	if key == "" {
		return "", usagef("--secret-key-file %s is empty", path)
	}
	return key, nil
}

// selfUpdate installs the newest release over this binary, or with --check
// only says whether there's one.
func selfUpdate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	check := fs.Bool("check", false, "only say whether a newer release is out")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError{err}
	}
	if fs.NArg() > 0 {
		return usagef("update takes no arguments")
	}
	r, err := update.Latest(ctx)
	if err != nil {
		return err
	}
	if !update.Newer(r.Version, version) {
		fmt.Printf("librepublish %s is the newest (the latest release is %s).\n", version, r.Version)
		return nil
	}
	fmt.Printf("librepublish %s is out (this is %s): %s\n", r.Version, version, r.URL)
	if *check {
		return nil
	}
	if update.InContainer() {
		return fmt.Errorf("this runs in a container: deploy ghcr.io/martin-business-consultants/librepublishgo:%s on your host instead", r.Version)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	if err := update.Install(ctx, r, exe); err != nil {
		return err
	}
	fmt.Printf("Installed %s at %s. Restart the service to run it (systemctl restart librepublish, or however it's started).\n", r.Version, exe)
	return nil
}

// handOverTo is the release serve stopped to hand over to (the update
// button), which main becomes once everything is closed.
var handOverTo string

// checkPersistent refuses, in a container, a data directory the next
// deploy would lose (no volume, or an anonymous one), unless EPHEMERAL_DATA=1
// says that's meant (a trial).
func checkPersistent(cfg cms.Config) error {
	if !update.InContainer() {
		return nil
	}
	if os.Getenv("EPHEMERAL_DATA") == "1" {
		cfg.Logger.Warn("EPHEMERAL_DATA=1: the data directory may be lost with this container", "data", cfg.DataDir)
		return nil
	}
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		cfg.Logger.Warn("can't tell whether the data directory is a volume", "err", err)
		return nil
	}
	defer f.Close()
	return datalock.CheckPersistent(cfg.DataDir, f)
}

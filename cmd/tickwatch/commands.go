package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"github.com/chaggangamer/tickwatch/internal/config"
	"github.com/chaggangamer/tickwatch/internal/store"
)

func openStoreForCLI(cfgPath string) (store.Store, config.Config, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, cfg, err
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(cfg.Database.Path, log)
	if err != nil {
		return nil, cfg, err
	}
	return st, cfg, nil
}

func runAdd(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: tickwatch add <http|tcp|ping|heartbeat> <target> [flags]")
	}
	kind := args[0]
	target := args[1]
	rest := args[2:]

	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	interval := fs.Int("interval", 60, "check interval in seconds")
	name := fs.String("name", "", "display name")
	timeout := fs.Int("timeout", 10, "timeout in seconds")
	cfgPath := fs.String("config", "", "path to config file")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	m := &store.Monitor{Name: *name, Interval: *interval, Timeout: *timeout, Enabled: true}
	switch kind {
	case store.TypeHTTP:
		raw, err := json.Marshal(store.HTTPConfig{URL: target, Method: "GET", ExpectStatus: "200-299"})
		if err != nil {
			return fmt.Errorf("marshal config: %w", err)
		}
		m.Type = store.TypeHTTP
		m.Config = raw
	case store.TypeTCP:
		host, port, err := splitHostPort(target)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(store.TCPConfig{Host: host, Port: port})
		if err != nil {
			return fmt.Errorf("marshal config: %w", err)
		}
		m.Type = store.TypeTCP
		m.Config = raw
	case store.TypePing:
		raw, err := json.Marshal(store.PingConfig{Host: target})
		if err != nil {
			return fmt.Errorf("marshal config: %w", err)
		}
		m.Type = store.TypePing
		m.Config = raw
	case store.TypeHeartbeat:
		return fmt.Errorf("create heartbeat monitors from the dashboard, they need a ping url")
	default:
		return fmt.Errorf("unknown monitor type %q", kind)
	}
	if m.Name == "" {
		m.Name = target
	}
	store.Normalize(m)
	if err := m.Validate(); err != nil {
		return err
	}

	st, _, err := openStoreForCLI(*cfgPath)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.CreateMonitor(m); err != nil {
		return err
	}
	fmt.Printf("added %s monitor %q (id %d, every %ds)\n", m.Type, m.Name, m.ID, m.Interval)
	return nil
}

func splitHostPort(s string) (string, int, error) {
	idx := -1
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			idx = i
			break
		}
	}
	if idx <= 0 || idx == len(s)-1 {
		return "", 0, fmt.Errorf("target must be host:port")
	}
	host := s[:idx]
	portStr := s[idx+1:]
	port := 0
	for _, c := range portStr {
		if c < '0' || c > '9' {
			return "", 0, fmt.Errorf("invalid port %q", portStr)
		}
		port = port*10 + int(c-'0')
	}
	if port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("port must be between 1 and 65535")
	}
	return host, port, nil
}

func runList(args []string) error {
	cfgPath := ""
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.StringVar(&cfgPath, "config", "", "path to config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, _, err := openStoreForCLI(cfgPath)
	if err != nil {
		return err
	}
	defer st.Close()
	monitors, err := st.Monitors()
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tTYPE\tNAME\tTARGET\tINTERVAL\tSTATUS\tENABLED")
	for _, m := range monitors {
		state := m.Status
		if !m.Enabled {
			state = "paused"
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%ds\t%s\t%t\n",
			m.ID, m.Type, m.Name, m.Target(), m.Interval, state, m.Enabled)
	}
	return w.Flush()
}

func runMigrate(args []string) error {
	cfgPath := ""
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.StringVar(&cfgPath, "config", "", "path to config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, cfg, err := openStoreForCLI(cfgPath)
	if err != nil {
		return err
	}
	defer st.Close()
	fmt.Printf("migrations applied, database at %s\n", cfg.Database.Path)
	return nil
}

func runToken(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: tickwatch token <add|list|delete> [args]")
	}
	cfgPath := ""
	fs := flag.NewFlagSet("token", flag.ContinueOnError)
	fs.StringVar(&cfgPath, "config", "", "path to config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) < 1 {
		return fmt.Errorf("usage: tickwatch token <add|list|delete> [args]")
	}

	st, _, err := openStoreForCLI(cfgPath)
	if err != nil {
		return err
	}
	defer st.Close()

	switch rest[0] {
	case "add":
		if len(rest) < 2 {
			return fmt.Errorf("usage: tickwatch token add <name>")
		}
		raw := store.NewToken(32)
		tok := &store.APIToken{Name: rest[1], TokenHash: store.HashToken(raw), CreatedAt: time.Now().Unix()}
		if err := st.CreateAPIToken(tok); err != nil {
			return err
		}
		fmt.Printf("token created, store it now, it is shown only once:\n%s\n", raw)
	case "list":
		tokens, err := st.APITokens()
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tCREATED\tLAST USED")
		for _, t := range tokens {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", t.ID, t.Name,
				time.Unix(t.CreatedAt, 0).Format("2006-01-02"),
				agoStr(t.LastUsedAt))
		}
		return w.Flush()
	case "delete":
		if len(rest) < 2 {
			return fmt.Errorf("usage: tickwatch token delete <id>")
		}
		id := 0
		for _, c := range rest[1] {
			if c < '0' || c > '9' {
				return fmt.Errorf("invalid token id %q", rest[1])
			}
			id = id*10 + int(c-'0')
		}
		if err := st.DeleteAPIToken(int64(id)); err != nil {
			return err
		}
		fmt.Printf("token %d deleted\n", id)
	default:
		return fmt.Errorf("unknown token command %q", rest[0])
	}
	return nil
}

func agoStr(unix int64) string {
	if unix <= 0 {
		return "never"
	}
	d := time.Since(time.Unix(unix, 0))
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh ago", int(d.Hours()))
}

func runHealthcheck(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: tickwatch healthcheck <url>")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(args[0])
	if err != nil {
		return fmt.Errorf("healthcheck failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("healthcheck failed: status %d", resp.StatusCode)
	}
	fmt.Println("healthy")
	return nil
}

package monitor

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
)

// Service checks: MQTT broker, PostgreSQL, MySQL/MariaDB and Redis. Logins come from a
// password credential (credentialId), filled into user/pass by the runner.

var defaultPorts = map[string]int{"mqtt": 1883, "postgres": 5432, "mysql": 3306, "redis": 6379}

func (c Config) addr(typ string) string {
	port := c.Port
	if port <= 0 {
		port = defaultPorts[typ]
		if typ == "mqtt" && c.TLS {
			port = 8883
		}
	}
	return net.JoinHostPort(c.Host, strconv.Itoa(port))
}

func short(s string) string {
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}

// checkMQTT connects to the broker; with a topic it also waits for a message (a
// retained one arrives at once) that must contain Keyword when set.
func checkMQTT(ctx context.Context, c Config) Result {
	start := time.Now()
	scheme := "tcp"
	if c.TLS {
		scheme = "ssl"
	}
	id := make([]byte, 4)
	rand.Read(id)
	opts := mqtt.NewClientOptions().AddBroker(scheme + "://" + c.addr("mqtt")).SetClientID("homewarden-monitor-" + hex.EncodeToString(id)).
		SetConnectTimeout(timeout(c)).SetAutoReconnect(false).SetConnectRetry(false).SetCleanSession(true)
	if c.TLS {
		opts.SetTLSConfig(&tls.Config{InsecureSkipVerify: c.Insecure})
	}
	if c.user != "" || c.pass != "" {
		opts.SetUsername(c.user).SetPassword(c.pass)
	}
	cl := mqtt.NewClient(opts)
	tok := cl.Connect()
	deadline, _ := ctx.Deadline()
	if !tok.WaitTimeout(time.Until(deadline)) {
		return Result{Msg: "connect timed out"}
	}
	if err := tok.Error(); err != nil {
		return Result{Msg: err.Error()}
	}
	defer cl.Disconnect(100)
	ms := float64(time.Since(start).Microseconds()) / 1000
	if c.Topic == "" {
		return Result{OK: true, Ms: ms, Msg: "connected"}
	}
	got := make(chan string, 16)
	sub := cl.Subscribe(c.Topic, 0, func(_ mqtt.Client, m mqtt.Message) {
		select {
		case got <- string(m.Payload()):
		default:
		}
	})
	if !sub.WaitTimeout(time.Until(deadline)) || sub.Error() != nil {
		return Result{Ms: ms, Msg: fmt.Sprintf("subscribe %s failed: %v", c.Topic, sub.Error())}
	}
	last := ""
	for {
		select {
		case p := <-got:
			last = p
			if c.Keyword == "" || strings.Contains(p, c.Keyword) {
				return Result{OK: true, Ms: ms, Msg: short(c.Topic + ": " + p)}
			}
		case <-ctx.Done():
			if last != "" {
				return Result{Ms: ms, Msg: short("keyword missing: " + c.Topic + ": " + last)}
			}
			return Result{Ms: ms, Msg: "no message on " + c.Topic}
		}
	}
}

func checkPostgres(ctx context.Context, c Config) Result {
	u := url.URL{Scheme: "postgres", Host: c.addr("postgres"), Path: "/" + c.Database}
	if c.user != "" {
		u.User = url.UserPassword(c.user, c.pass)
	}
	q := url.Values{"sslmode": {"prefer"}}
	if c.TLS {
		q.Set("sslmode", "require")
	}
	u.RawQuery = q.Encode()
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		// pgx lists every attempt (TLS first, then plain) on its own line; the last
		// one holds the real reason.
		lines := strings.Split(strings.TrimSpace(err.Error()), "\n")
		last := strings.TrimSpace(lines[len(lines)-1])
		if _, after, ok := strings.Cut(last, "): "); ok {
			last = after
		}
		return Result{Msg: short(last)}
	}
	defer conn.Close(context.Background())
	var one int
	if err := conn.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
		return Result{Msg: short(err.Error())}
	}
	return Result{OK: true, Msg: "SELECT 1"}
}

func checkMySQL(ctx context.Context, c Config) Result {
	cfg := mysql.NewConfig()
	cfg.Net, cfg.Addr, cfg.User, cfg.Passwd, cfg.DBName = "tcp", c.addr("mysql"), c.user, c.pass, c.Database
	cfg.Timeout = timeout(c)
	cfg.AllowNativePasswords = true
	if c.TLS {
		cfg.TLSConfig = "true"
		if c.Insecure {
			cfg.TLSConfig = "skip-verify"
		}
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return Result{Msg: err.Error()}
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	var one int
	if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		return Result{Msg: short(err.Error())}
	}
	return Result{OK: true, Msg: "SELECT 1"}
}

// checkRedis sends AUTH (when a login is set) and PING over RESP.
func checkRedis(ctx context.Context, c Config) Result {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", c.addr("redis"))
	if err != nil {
		return Result{Msg: err.Error()}
	}
	if c.TLS {
		host, _, _ := net.SplitHostPort(c.addr("redis"))
		conn = tls.Client(conn, &tls.Config{ServerName: host, InsecureSkipVerify: c.Insecure})
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	r := bufio.NewReader(conn)
	cmd := func(args ...string) (string, error) {
		var b strings.Builder
		fmt.Fprintf(&b, "*%d\r\n", len(args))
		for _, a := range args {
			fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
		}
		if _, err := conn.Write([]byte(b.String())); err != nil {
			return "", err
		}
		line, err := r.ReadString('\n')
		return strings.TrimSpace(line), err
	}
	if c.pass != "" {
		args := []string{"AUTH", c.pass}
		if c.user != "" {
			args = []string{"AUTH", c.user, c.pass}
		}
		resp, err := cmd(args...)
		if err != nil {
			return Result{Msg: err.Error()}
		}
		if resp != "+OK" {
			return Result{Msg: short(strings.TrimPrefix(resp, "-"))}
		}
	}
	resp, err := cmd("PING")
	if err != nil {
		return Result{Msg: err.Error()}
	}
	if resp != "+PONG" {
		return Result{Msg: short(strings.TrimPrefix(resp, "-"))}
	}
	return Result{OK: true, Msg: "PONG"}
}

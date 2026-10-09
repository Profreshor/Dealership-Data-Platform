// Package smoke runs the bounded T1 proving-ground path.
package smoke

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/auth"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/models"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/serving"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Report struct {
	Status   string `json:"status"`
	Database string `json:"database"`
	Job      string `json:"job"`
	Model    string `json:"model"`
	Page     string `json:"page"`
}

type plan struct{ job, model, page string }

func Run(ctx context.Context, cfg *config.Config, registryPath string, log io.Writer, register func(*web.Registry)) (report Report, err error) {
	p, err := smokePlan(cfg)
	if err != nil {
		return Report{}, err
	}
	baseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if baseURL == "" {
		return Report{}, errors.New("smoke: TEST_DATABASE_URL is required")
	}
	if _, err = databaseURL(baseURL, "preflight", "", ""); err != nil {
		return Report{}, errors.New("smoke: TEST_DATABASE_URL must be a PostgreSQL URL")
	}
	parsedURL, _ := url.Parse(baseURL)
	parameters, err := url.ParseQuery(parsedURL.RawQuery)
	if err != nil {
		return Report{}, errors.New("smoke: invalid TEST_DATABASE_URL query parameters")
	}
	for key := range parameters {
		switch strings.ToLower(key) {
		case "database", "dbname", "host", "hostaddr", "options", "password", "port", "role", "service", "servicefile", "user":
			return Report{}, errors.New("smoke: TEST_DATABASE_URL must put host, credentials and database in the URL authority and path; connection overrides are unsupported")
		}
	}
	for _, read := range cfg.Jobs[strings.TrimPrefix(p.job, "job/")].Reads {
		if name, ok := strings.CutPrefix(read, "integration/"); ok {
			integration := cfg.Integrations[name]
			if integration.Auth != nil && os.Getenv(integration.Auth.Secret) == "" {
				return Report{}, fmt.Errorf("smoke: required integration secret %s is missing", integration.Auth.Secret)
			}
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return Report{}, errors.New("smoke: locate ddp executable")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return Report{}, errors.New("smoke: node is required")
	}
	registryPath, err = filepath.Abs(registryPath)
	if err != nil {
		return Report{}, fmt.Errorf("smoke: resolve registry path: %w", err)
	}
	root := filepath.Dir(registryPath)
	if _, err = os.Stat(filepath.Join(root, "frontend", "smoke.mjs")); err != nil {
		return Report{}, errors.New("smoke: frontend/smoke.mjs is required")
	}
	if log == nil {
		log = io.Discard
	}

	adminCfg, err := pgx.ParseConfig(baseURL)
	if err != nil {
		return Report{}, errors.New("smoke: invalid TEST_DATABASE_URL")
	}
	admin, err := pgx.ConnectConfig(ctx, adminCfg)
	if err != nil {
		return Report{}, errors.New("smoke: cannot connect to TEST_DATABASE_URL")
	}
	defer admin.Close(context.Background())

	database := "ddp_smoke_" + strings.ToLower(rand.Text())
	login := "ddp_smoke_login_" + strings.ToLower(rand.Text())
	password := rand.Text() + rand.Text()
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{database}.Sanitize()); err != nil {
		return Report{}, errors.New("smoke: create disposable database failed")
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var cleanupErr error
		if _, e := admin.Exec(cleanupCtx, "DROP DATABASE "+pgx.Identifier{database}.Sanitize()+" WITH (FORCE)"); e != nil {
			cleanupErr = errors.Join(cleanupErr, errors.New("drop disposable database failed"))
		}
		if _, e := admin.Exec(cleanupCtx, "DROP ROLE IF EXISTS "+pgx.Identifier{login}.Sanitize()); e != nil {
			cleanupErr = errors.Join(cleanupErr, errors.New("drop temporary login failed"))
		}
		if cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("smoke cleanup: %w", cleanupErr))
		}
	}()

	dbCfg := *adminCfg
	dbCfg.Database = database
	if err = runPhase(log, "migrate", func() error {
		conn, e := pgx.ConnectConfig(ctx, &dbCfg)
		if e != nil {
			return errors.New("connect disposable database failed")
		}
		defer conn.Close(context.Background())
		if e = migrate.Up(ctx, conn); e != nil {
			return fmt.Errorf("apply embedded migrations: %w", e)
		}
		return nil
	}); err != nil {
		return Report{}, err
	}
	if err = createJobLogin(ctx, admin, login, password); err != nil {
		return Report{}, err
	}

	schedulerPool, err := rolePool(ctx, &dbCfg, "ddp_scheduler")
	if err != nil {
		return Report{}, errors.New("smoke: scheduler database configuration failed")
	}
	defer schedulerPool.Close()
	apiPool, err := rolePool(ctx, &dbCfg, "ddp_api")
	if err != nil {
		return Report{}, errors.New("smoke: API database configuration failed")
	}
	defer apiPool.Close()
	if err = schedulerPool.Ping(ctx); err != nil {
		return Report{}, errors.New("smoke: scheduler database connection failed")
	}
	if err = apiPool.Ping(ctx); err != nil {
		return Report{}, errors.New("smoke: API database connection failed")
	}

	jobURL, err := databaseURL(baseURL, database, login, password)
	if err != nil {
		return Report{}, errors.New("smoke: build job database URL failed")
	}
	schedulerURL, err := databaseURL(baseURL, database, "", "")
	if err != nil {
		return Report{}, errors.New("smoke: build scheduler database URL failed")
	}
	u, _ := url.Parse(schedulerURL)
	q := u.Query()
	q.Set("options", "-c role=ddp_scheduler")
	u.RawQuery = q.Encode()
	schedulerURL = u.String()
	if err = runPhase(log, "ingest", func() error {
		cmd := exec.Command(executable, "--config", registryPath, "--json", "jobs", "run", p.job)
		cmd.Dir = root
		cmd.Env = withEnv(os.Environ(), "DATABASE_URL", schedulerURL, "JOB_DATABASE_URL", jobURL)
		if e := runCommand(ctx, cmd); e != nil {
			return errors.New("job command failed")
		}
		return nil
	}); err != nil {
		return Report{}, fmt.Errorf("smoke ingest: %w", err)
	}
	if err = runPhase(log, "models", func() error {
		if _, e := models.Apply(ctx, schedulerPool, cfg, root); e != nil {
			return e
		}
		return models.Verify(ctx, schedulerPool, cfg)
	}); err != nil {
		return Report{}, fmt.Errorf("smoke models: %w", err)
	}

	email, adminPassword := "smoke-"+strings.ToLower(rand.Text())+"@example.test", rand.Text()+rand.Text()
	if err = runPhase(log, "bootstrap", func() error { return auth.New(apiPool, 8*time.Hour).Bootstrap(ctx, email, adminPassword) }); err != nil {
		return Report{}, fmt.Errorf("smoke bootstrap: %w", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Report{}, errors.New("smoke: listen on loopback failed")
	}
	defer ln.Close()
	origin := "http://" + ln.Addr().String()
	cfgCopy := *cfg
	cfgCopy.Serving.PublicURL = origin
	handler, err := serving.PortalHandler(apiPool, &cfgCopy, slog.New(slog.NewJSONHandler(log, nil)), register)
	if err != nil {
		return Report{}, fmt.Errorf("smoke portal: %w", err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if e := server.Shutdown(shutdownCtx); e != nil {
			err = errors.Join(err, errors.New("smoke: portal shutdown failed"))
		}
	}()
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(ln) }()
	select {
	case e := <-serveDone:
		if e != nil && !errors.Is(e, http.ErrServerClosed) {
			return Report{}, errors.New("smoke: portal failed to start")
		}
	default:
	}
	if err = runBrowser(ctx, node, root, origin, email, adminPassword, p.page); err != nil {
		return Report{}, err
	}
	logf(log, "browser", "passed")
	return Report{Status: "passed", Database: database, Job: p.job, Model: p.model, Page: p.page}, nil
}

func smokePlan(c *config.Config) (plan, error) {
	if c == nil {
		return plan{}, errors.New("smoke: config is required")
	}
	jobs := []string{}
	for name, job := range c.Jobs {
		if job.Action == "ingest" {
			jobs = append(jobs, "job/"+name)
		}
	}
	if len(jobs) != 1 {
		return plan{}, fmt.Errorf("smoke: registry must contain exactly one ingest job (found %d)", len(jobs))
	}
	writes := map[string]bool{}
	for _, write := range c.Jobs[strings.TrimPrefix(jobs[0], "job/")].Writes {
		writes[write.Target] = true
	}
	type pagePath struct {
		path  string
		model string
	}
	pagePaths := []pagePath{}
	tablePages := 0
	unknownModelPage := false
	for _, page := range c.Pages {
		endpoint, ok := c.Endpoints[strings.TrimPrefix(page.Endpoint, "endpoint/")]
		if page.Kind != "table" || !ok || len(endpoint.Reads) != 1 {
			continue
		}
		tablePages++
		ref := endpoint.Reads[0]
		if !strings.HasPrefix(ref, "model/") {
			continue
		}
		if _, ok := c.Models[strings.TrimPrefix(ref, "model/")]; !ok {
			unknownModelPage = true
			continue
		}
		pagePaths = append(pagePaths, pagePath{path: page.Path, model: ref})
	}

	sort.Slice(pagePaths, func(i, j int) bool {
		if pagePaths[i].model == pagePaths[j].model {
			return pagePaths[i].path < pagePaths[j].path
		}
		return pagePaths[i].model < pagePaths[j].model
	})
	if tablePages == 0 {
		return plan{}, errors.New("smoke: ingest path must contain exactly one table page (found 0)")
	}
	if unknownModelPage {
		return plan{}, errors.New("smoke: ingest path must contain exactly one model (found 0)")
	}
	if len(pagePaths) == 0 {
		return plan{}, errors.New("smoke: ingest path must contain exactly one table page (found 0)")
	}
	eligible := []pagePath{}
	for _, candidate := range pagePaths {
		reaches, err := modelReachesWrites(c, candidate.model, writes, map[string]bool{}, map[string]int{})
		if err != nil {
			return plan{}, err
		}
		if reaches {
			eligible = append(eligible, candidate)
		}
	}
	modelRefs := map[string]bool{}
	for _, candidate := range eligible {
		modelRefs[candidate.model] = true
	}
	if len(modelRefs) != 1 {
		return plan{}, fmt.Errorf("smoke: ingest path must contain exactly one model (found %d)", len(modelRefs))
	}
	if len(eligible) != 1 {
		return plan{}, fmt.Errorf("smoke: ingest path must contain exactly one table page (found %d)", len(eligible))
	}
	return plan{job: jobs[0], model: eligible[0].model, page: eligible[0].path}, nil
}

func modelReachesWrites(c *config.Config, ref string, writes map[string]bool, visiting map[string]bool, done map[string]int) (bool, error) {
	if visiting[ref] {
		return false, fmt.Errorf("smoke: model dependency cycle at %s", ref)
	}
	if state, ok := done[ref]; ok {
		return state == 2, nil
	}
	model, ok := c.Models[strings.TrimPrefix(ref, "model/")]
	if !ok {
		return false, fmt.Errorf("smoke: model dependency %s is unknown", ref)
	}
	visiting[ref] = true
	defer delete(visiting, ref)
	reachesWrites := false
	for _, read := range model.Reads {
		if writes[read] {
			reachesWrites = true
			continue
		}
		if strings.HasPrefix(read, "model/") {
			reaches, err := modelReachesWrites(c, read, writes, visiting, done)
			if err != nil {
				return false, err
			}
			reachesWrites = reachesWrites || reaches
			continue
		}
		if strings.HasPrefix(read, "table/") {
			continue
		}
		return false, fmt.Errorf("smoke: model %s references unknown relation %s", ref, read)
	}
	if reachesWrites {
		done[ref] = 2
	} else {
		done[ref] = 1
	}
	return reachesWrites, nil
}

func rolePool(ctx context.Context, c *pgx.ConnConfig, role string) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(c.ConnString())
	if err != nil {
		return nil, err
	}
	pc.ConnConfig = c.Copy()
	pc.ConnConfig.RuntimeParams["role"] = role
	return pgxpool.NewWithConfig(ctx, pc)
}

func createJobLogin(ctx context.Context, admin *pgx.Conn, login, password string) error {
	var sql string
	if err := admin.QueryRow(ctx, "SELECT format('CREATE ROLE %I LOGIN PASSWORD %L', $1::text, $2::text)", login, password).Scan(&sql); err != nil {
		return errors.New("smoke: prepare temporary login failed")
	}
	if _, err := admin.Exec(ctx, sql); err != nil {
		return errors.New("smoke: create temporary login failed")
	}
	if _, err := admin.Exec(ctx, "GRANT ddp_job TO "+pgx.Identifier{login}.Sanitize()); err != nil {
		return errors.New("smoke: grant job role failed")
	}
	return nil
}

func databaseURL(raw, database, user, password string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		return "", errors.New("invalid PostgreSQL URL")
	}
	u.Path = "/" + database
	q := u.Query()
	for _, key := range []string{"database", "dbname", "host", "hostaddr", "options", "password", "port", "role", "service", "servicefile", "user"} {
		q.Del(key)
	}
	u.RawQuery = q.Encode()
	if user != "" {
		u.User = url.UserPassword(user, password)
	}
	return u.String(), nil
}

func withEnv(env []string, pairs ...string) []string {
	replaced := make(map[string]bool, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		replaced[pairs[i]] = true
	}
	out := make([]string, 0, len(env)+len(replaced))
	for _, value := range env {
		key, _, _ := strings.Cut(value, "=")
		if !replaced[key] {
			out = append(out, value)
		}
	}
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, pairs[i]+"="+pairs[i+1])
	}
	return out
}

func runCommand(ctx context.Context, cmd *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 3 * time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) //nolint:errcheck -- reap any descendant left after the command exits.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		return ctx.Err()
	}
}

func runPhase(w io.Writer, phase string, fn func() error) error {
	logf(w, phase, "started")
	if err := fn(); err != nil {
		logf(w, phase, "failed")
		return err
	}
	logf(w, phase, "passed")
	return nil
}

func logf(w io.Writer, phase, state string) {
	_, _ = fmt.Fprintf(w, "smoke phase=%s state=%s\n", phase, state)
}

func runBrowser(ctx context.Context, node, root, origin, email, password, page string) error {
	cmd := exec.Command(node, filepath.Join(root, "frontend", "smoke.mjs"))
	cmd.Dir = root
	cmd.Env = withEnv(os.Environ(), "DDP_SMOKE_ORIGIN", origin, "DDP_SMOKE_EMAIL", email, "DDP_SMOKE_PASSWORD", password, "DDP_SMOKE_PAGE", page)
	if err := runCommand(ctx, cmd); err != nil {
		return errors.New("smoke browser: Playwright failed")
	}
	return nil
}

package onboard

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5"
	"go.yaml.in/yaml/v3"
)

type Options struct {
	Template, Directory, Discovery string
	DryRun                         bool
}
type Result struct {
	Directory string   `json:"directory"`
	Project   string   `json:"project"`
	Files     []string `json:"files"`
	DryRun    bool     `json:"dry_run"`
}

// Initialize reads only local discovery and source files. A new directory is
// reserved exclusively; failures retain it for inspection instead of deleting data.
func Initialize(ctx context.Context, options Options) (Result, error) {
	var result Result
	input, err := os.Open(options.Discovery)
	if err != nil {
		return result, fmt.Errorf("read discovery: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(input, (1<<20)+1))
	closeErr := input.Close()
	err = errors.Join(err, closeErr)
	if err != nil {
		return result, fmt.Errorf("read discovery: %w", err)
	}
	discovery, err := ParseDiscovery(data)
	if err != nil {
		return result, err
	}
	target, err := filepath.Abs(options.Directory)
	if err != nil {
		return result, err
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return result, errors.New("initialization requires a new directory")
		}
		return result, err
	}
	// Use a clean tracked inventory so template_revision identifies the copied
	// source, and ignored credentials and local build outputs never enter it.
	git := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", options.Template}, args...)...)
		for _, item := range os.Environ() {
			if !strings.HasPrefix(item, "GIT_") {
				cmd.Env = append(cmd.Env, item)
			}
		}
		out, e := cmd.Output()
		if e != nil {
			return nil, errors.New("template must be a local Git checkout with HEAD")
		}
		return out, nil
	}
	revision, err := git("rev-parse", "--verify", "HEAD")
	if err != nil {
		return result, err
	}
	prefix, err := git("rev-parse", "--show-prefix")
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(string(prefix)) != "" {
		return result, errors.New("template must be the root of its Git checkout")
	}
	status, err := git("status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return result, err
	}
	if len(status) != 0 {
		return result, errors.New("template has uncommitted tracked changes; commit them before initialization")
	}
	archive, err := git("archive", "--format=tar", strings.TrimSpace(string(revision)))
	if err != nil {
		return result, err
	}
	files := map[string][]byte{}
	modes := map[string]fs.FileMode{}
	reader := tar.NewReader(bytes.NewReader(archive))
	for {
		entry, e := reader.Next()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return result, e
		}
		name := entry.Name
		if name == ".agents/skills" || name == ".claude/skills" {
			if entry.Typeflag != tar.TypeSymlink || entry.Linkname != "../skills" {
				return result, fmt.Errorf("skill discovery link must point to ../skills: %s", name)
			}
			files[name], modes[name] = []byte(entry.Linkname), fs.ModeSymlink
			continue
		}
		if !templateFile(name) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !entry.FileInfo().Mode().IsRegular() {
			return result, fmt.Errorf("template file must be regular: %s", name)
		}
		content, e := io.ReadAll(reader)
		if e != nil {
			return result, e
		}
		files[name], modes[name] = content, entry.FileInfo().Mode().Perm()
	}
	for _, required := range []string{"AGENTS.md", "go.mod", "Makefile", "ddp.yaml", "deploy/Dockerfile", "migrations/embed.go"} {
		if _, ok := files[required]; !ok {
			return result, fmt.Errorf("template is missing %s", required)
		}
	}
	cfg, err := config.Parse(files["ddp.yaml"])
	if err != nil {
		return result, err
	}
	if len(cfg.Integrations)+len(cfg.Jobs)+len(cfg.Tables)+len(cfg.Models)+len(cfg.Endpoints) != 0 {
		return result, errors.New("initialize from the empty DDP template, not a configured client")
	}
	generated, err := generate(discovery, cfg, strings.TrimSpace(string(revision)), data)
	if err != nil {
		return result, err
	}
	for name, content := range generated {
		if _, exists := files[name]; exists && (strings.HasPrefix(name, "client/") || strings.HasPrefix(name, "migrations/")) {
			return result, fmt.Errorf("generated file conflicts with template source: %s", name)
		}
		files[name] = content
		modes[name] = 0644
	}
	result = Result{Directory: target, Project: cfg.Ddp.Name, Files: make([]string, 0, len(files)), DryRun: options.DryRun}
	for name := range files {
		result.Files = append(result.Files, name)
	}
	slices.Sort(result.Files)
	if options.DryRun {
		return result, nil
	}
	if err := os.Mkdir(target, 0755); err != nil {
		return result, err
	}
	destination, err := os.OpenRoot(target)
	if err != nil {
		return result, err
	}
	defer destination.Close()
	for _, name := range result.Files {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := destination.MkdirAll(filepath.Dir(name), 0755); err != nil {
			return result, err
		}
		if modes[name] == fs.ModeSymlink {
			if err := destination.Symlink(string(files[name]), name); err != nil {
				return result, err
			}
			continue
		}
		file, e := destination.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, modes[name])
		if e != nil {
			return result, e
		}
		_, writeErr := file.Write(files[name])
		closeErr := file.Close()
		if e = errors.Join(writeErr, closeErr); e != nil {
			return result, e
		}
	}
	if _, err := config.Load(filepath.Join(target, "ddp.yaml")); err != nil {
		return result, err
	}
	return result, nil
}

func templateFile(name string) bool {
	if !fs.ValidPath(name) {
		return false
	}
	if strings.HasPrefix(name, "tests/proving-ground/") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".env") || slices.Contains([]string{".git", ".venv", "node_modules", "dist", "__pycache__", "test-results", "playwright-report"}, part) {
			return false
		}
	}
	top, _, _ := strings.Cut(name, "/")
	return slices.Contains([]string{"AGENTS.md", "CLAUDE.md", "README.md", "VISION.md", "DECISIONS.md", "ARCHITECTURE.md", "Makefile", "ddp.yaml", "go.mod", "go.sum", "pyproject.toml", "uv.lock", ".gitignore", ".gitleaks.toml", ".python-version", ".dockerignore", ".github", ".devcontainer", "cmd", "internal", "migrations", "frontend", "ddp", "client", "jobs", "models", "health", "templates", "schema", "tests", "deploy", "bin", "docs", "skills"}, top)
}

var projectSeparators = regexp.MustCompile(`[^a-z0-9]+`)

func generate(d Discovery, cfg *config.Config, revision string, raw []byte) (map[string][]byte, error) {
	name := strings.Trim(projectSeparators.ReplaceAllString(strings.ToLower(d.Client.Name), "_"), "_")
	if name == "" || name[0] < 'a' || name[0] > 'z' {
		return nil, errors.New("client.name must produce an identifier beginning with a letter")
	}
	cfg.Ddp = config.Identity{Name: name, DisplayName: d.Client.Name, Timezone: d.Client.Timezone, TemplateRevision: revision}
	cfg.Serving.PublicURL = "https://" + d.Hosting.Domain
	repository := strings.TrimPrefix(d.Hosting.Repository, "https://github.com/")
	cfg.Deploy = config.Deploy{Image: "ghcr.io/" + strings.ToLower(repository), Channel: "stable", Edge: "cloudflare"}
	cfg.Comms = config.Comms{Groups: map[string]config.Group{}}
	for _, group := range d.Communications.Groups {
		cfg.Comms.Groups[group.Name] = config.Group{Recipients: group.Recipients}
	}
	if _, ok := cfg.Comms.Groups["platform_ops"]; !ok {
		return nil, errors.New("discovery communications.groups must declare platform_ops for platform alerts")
	}
	files := map[string][]byte{"docs/discovery.yaml": raw}
	var migration strings.Builder
	migration.WriteString("-- Source landing schemas from discovery. Ingest jobs require verified access.\n")
	for _, system := range d.Systems {
		cfg.Integrations[system.Name] = config.Integration{Kind: system.Kind, Docs: system.DocsURL, Settings: map[string]any{"vendor": system.Vendor, "access": system.Access, "credential_owner": system.CredentialOwner}}
		schema := pgx.Identifier{system.Name}.Sanitize()
		fmt.Fprintf(&migration, "CREATE SCHEMA %s AUTHORIZATION ddp_owner;\nGRANT USAGE ON SCHEMA %s TO ddp_job, ddp_scheduler, ddp_readonly;\n", schema, schema)
		files["client/"+system.Name+".py"] = []byte("\"\"\"Integration " + system.Name + ".\n\nRead docs/discovery.yaml and verify access before implementation.\n\"\"\"\n")
	}
	migration.WriteString(accountMigration(d))
	if len(d.Systems)+len(d.Users) > 0 {
		files["migrations/app/"+time.Now().UTC().Format("20060102150405")+"_discovery.sql"] = []byte(migration.String())
	}
	normalized, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(normalized, &document); err != nil {
		return nil, err
	}
	// JSON provides the authoritative field tags; emit editable block YAML.
	var block func(*yaml.Node)
	block = func(n *yaml.Node) {
		n.Style = 0
		for _, child := range n.Content {
			block(child)
		}
	}
	block(&document)
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return nil, err
	}
	if _, err := config.Parse(out.Bytes()); err != nil {
		return nil, err
	}
	files["ddp.yaml"] = out.Bytes()
	files["docs/onboarding-plan.md"] = []byte(onboardingPlan(d))
	files["tests/project.mk"] = []byte("# Client-owned extra Python check scopes.\nPROJECT_PYTHON := tests/check-smoke.py\nPROJECT_PYRIGHT :=\n")
	files["tests/check-smoke.py"] = []byte(clientSmoke)
	files["tests/check-image.sh"] = []byte(clientImage)
	files[".env.example"] = []byte(`# Secret names only. Never commit populated environment files.
# Database passwords: generate each independently with openssl rand -hex 32.
POSTGRES_PASSWORD=
OWNER_DATABASE_PASSWORD=
API_DATABASE_PASSWORD=
SCHEDULER_DATABASE_PASSWORD=
JOB_DATABASE_PASSWORD=
BACKUP_DATABASE_PASSWORD=
READONLY_DATABASE_PASSWORD=
TUNNEL_TOKEN=
DDP_IMAGE_DIGEST=
BACKUP_ACCESS_KEY_ID=
BACKUP_SECRET_ACCESS_KEY=
BACKUP_AGE_IDENTITY=
# Optional host paths (defaults are documented in docs/deployment.md).
POSTGRES_DATA_PATH=
BACKUP_STAGING_PATH=
# Direct CLI connections, when needed outside production Compose.
DATABASE_URL=
SCHEDULER_DATABASE_URL=
JOB_DATABASE_URL=
OWNER_DATABASE_URL=
BACKUP_DATABASE_URL=
`)
	files["README.md"] = []byte("# " + d.Client.Name + "\n\nRead [the onboarding plan](docs/onboarding-plan.md) and [discovery facts](docs/discovery.yaml).\n\nRun `make setup`, `make db`, `make build`, and `build/ddp validate`. Add `build/` to your PATH to use `ddp` directly. Scheduling and email stay off unless explicitly enabled.\n\nUse the [canonical action procedures](skills/) for development and operation. `CLAUDE.md` points to the unchanged `AGENTS.md`; the `.claude/skills` and `.agents/skills` directories link to the same canonical files.\n\nVerify ingest, reporting, deployment and recovery before relying on this system.\n")
	return files, nil
}

func onboardingPlan(d Discovery) string {
	var out strings.Builder
	out.WriteString("# Onboarding plan\n\nDiscovery is recorded in [discovery.yaml](discovery.yaml). This repository has no working ingest jobs or business reports yet.\n\n")
	out.WriteString("1. Verify each system's authentication, usable access, pagination, rate limits, update cursor and deletion behavior. Record evidence before writing a job.\n2. Declare landing tables and idempotent ingest jobs with `ddp new`; test them against synthetic data.\n3. Define staging, core and mart models from the agreed questions, then declare protected endpoints and pages.\n4. Apply migrations and bootstrap the first operator with `ddp users bootstrap`. Discovered accounts and roles are seeded without permissions; assign approved permissions to those roles, then invite the remaining users.\n5. Configure SMTP, requested emails, freshness rules and alert routing.\n6. Run `ddp check` and the hard `ddp smoke` gate. Complete deployment, backup and restore verification before going live.\n\n## Questions\n\n")
	for _, question := range d.Questions {
		fmt.Fprintf(&out, "- %s\n", question)
	}
	out.WriteString("\n## Requested pages\n\n")
	for _, page := range d.Outputs.Pages {
		fmt.Fprintf(&out, "- %s\n", page)
	}
	out.WriteString("\n## Acceptance\n\n")
	for _, condition := range d.Success30Days {
		fmt.Fprintf(&out, "- %s\n", condition)
	}
	return out.String()
}

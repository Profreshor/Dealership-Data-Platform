// Package config owns the project registry and its generated schema.
package config

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/schedule"
	"github.com/invopop/jsonschema"
	validator "github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

type Config struct {
	Retention    *Retention             `json:"retention,omitempty"`
	Ddp          Identity               `json:"ddp"`
	Database     Database               `json:"database"`
	Scheduler    Scheduler              `json:"scheduler"`
	Integrations map[string]Integration `json:"integrations"`
	Jobs         map[string]Job         `json:"jobs"`
	Tables       map[string]Table       `json:"tables"`
	Models       map[string]Model       `json:"models"`
	Health       map[string]Health      `json:"health"`
	Endpoints    map[string]Endpoint    `json:"endpoints"`
	Pages        map[string]Page        `json:"pages"`
	Comms        Comms                  `json:"comms"`
	Permissions  map[string]Permission  `json:"permissions"`
	Serving      Serving                `json:"serving"`
	Deploy       Deploy                 `json:"deploy"`
}

type Identity struct {
	TemplateRevision string    `json:"template_revision" jsonschema:"pattern=^[a-f0-9]{40}$"`
	Name             string    `json:"name" jsonschema:"pattern=^[a-z][a-z0-9_-]*$"`
	DisplayName      string    `json:"display_name" jsonschema:"minLength=1"`
	Timezone         string    `json:"timezone" jsonschema:"minLength=1"`
	Branding         *Branding `json:"branding,omitempty"`
}
type Branding struct {
	Logo   string `json:"logo,omitempty" jsonschema:"pattern=^frontend/apps/portal/public/[A-Za-z0-9_./-]+[.](svg|png|jpg|jpeg|webp|gif)$"`
	Accent string `json:"accent,omitempty" jsonschema:"pattern=^#[a-fA-F0-9]{6}$"`
}
type Database struct {
	Layers []string `json:"layers" jsonschema:"minItems=3,maxItems=3"`
}
type Scheduler struct {
	MaxWorkers int      `json:"max_workers" jsonschema:"minimum=1"`
	Defaults   Defaults `json:"defaults"`
}
type Defaults struct {
	Timeout string `json:"timeout"`
	Retry   Retry  `json:"retry"`
	Catchup string `json:"catchup" jsonschema:"enum=none,enum=latest_only"`
}
type Retry struct {
	MaxAttempts  int    `json:"max_attempts" jsonschema:"minimum=1"`
	InitialDelay string `json:"initial_delay"`
	MaxDelay     string `json:"max_delay"`
}
type Integration struct {
	Kind     string           `json:"kind" jsonschema:"minLength=1"`
	BaseURL  string           `json:"base_url,omitempty"`
	Docs     string           `json:"docs,omitempty"`
	Auth     *IntegrationAuth `json:"auth,omitempty"`
	Settings map[string]any   `json:"settings,omitempty"`
}
type IntegrationAuth struct {
	Type   string `json:"type" jsonschema:"enum=api_key,enum=bearer"`
	Header string `json:"header,omitempty"`
	Secret string `json:"secret" jsonschema:"pattern=^[A-Z][A-Z0-9_]*$"`
}
type Idempotency struct {
	Strategy string `json:"strategy" jsonschema:"enum=provider_key,enum=natural_key,enum=reconcile,enum=duplicates_acceptable"`
	Reason   string `json:"reason,omitempty"`
}

type Job struct {
	Idempotency    *Idempotency   `json:"idempotency,omitempty"`
	Purpose        string         `json:"purpose" jsonschema:"minLength=1"`
	Action         string         `json:"action" jsonschema:"enum=ingest,enum=transform,enum=check,enum=export,enum=notify,enum=operate,enum=maintain"`
	Python         string         `json:"python,omitempty" jsonschema:"pattern=^jobs(\\.[a-z_][a-z0-9_]*)+$"`
	Model          string         `json:"model,omitempty"`
	Reads          []string       `json:"reads,omitempty"`
	Writes         []Write        `json:"writes,omitempty"`
	After          []string       `json:"after,omitempty"`
	Deletions      string         `json:"deletions,omitempty" jsonschema:"enum=ignore,enum=tombstone,enum=reconcile,enum=replace"`
	Schedule       string         `json:"schedule,omitempty"`
	Timeout        string         `json:"timeout,omitempty"`
	Retry          *Retry         `json:"retry,omitempty"`
	Catchup        string         `json:"catchup,omitempty" jsonschema:"enum=none,enum=latest_only"`
	ConcurrencyKey string         `json:"concurrency_key,omitempty"`
	Tags           []string       `json:"tags,omitempty"`
	Settings       map[string]any `json:"settings,omitempty"`
}
type Write struct {
	Target string   `json:"target"`
	Mode   string   `json:"mode" jsonschema:"enum=upsert,enum=replace,enum=append"`
	Key    []string `json:"key,omitempty"`
}
type Table struct {
	Purpose  string   `json:"purpose" jsonschema:"minLength=1"`
	Contract Contract `json:"contract"`
}
type Contract struct {
	Columns    map[string]Column `json:"columns" jsonschema:"minProperties=1"`
	PrimaryKey []string          `json:"primary_key,omitempty"`
	UniqueKey  []string          `json:"unique_key,omitempty"`
}
type Column struct {
	Type        string `json:"type" jsonschema:"minLength=1"`
	Nullable    bool   `json:"nullable"`
	Description string `json:"description,omitempty"`
}
type Model struct {
	File            string   `json:"file" jsonschema:"minLength=1"`
	Purpose         string   `json:"purpose" jsonschema:"minLength=1"`
	Materialization string   `json:"materialization" jsonschema:"enum=view,enum=materialized_view"`
	Reads           []string `json:"reads" jsonschema:"minItems=1"`
	Contract        Contract `json:"contract"`
	Schedule        string   `json:"schedule,omitempty"`
}
type Health struct {
	Kind     string   `json:"kind" jsonschema:"enum=freshness,enum=sql"`
	Target   string   `json:"target,omitempty"`
	Column   string   `json:"column,omitempty"`
	MaxAge   string   `json:"max_age,omitempty"`
	SQL      string   `json:"sql,omitempty"`
	Severity string   `json:"severity" jsonschema:"enum=warning,enum=critical"`
	Notify   []string `json:"notify" jsonschema:"minItems=1"`
}
type Endpoint struct {
	Reads     []string `json:"reads" jsonschema:"minItems=1,maxItems=1"`
	Path      string   `json:"path" jsonschema:"pattern=^/api/"`
	Policy    string   `json:"policy" jsonschema:"minLength=1"`
	Columns   []string `json:"columns" jsonschema:"minItems=1"`
	Filters   []string `json:"filters,omitempty"`
	Sort      []string `json:"sort,omitempty"`
	Search    []string `json:"search,omitempty"`
	UniqueKey []string `json:"unique_key,omitempty"`
	PageSize  int      `json:"page_size,omitempty" jsonschema:"minimum=1,maximum=1000"`
	Shape     string   `json:"shape,omitempty" jsonschema:"enum=singleton"`
	Export    *Export  `json:"export,omitempty"`
}
type Export struct {
	Format  string `json:"format" jsonschema:"enum=csv"`
	MaxRows int    `json:"max_rows" jsonschema:"minimum=1"`
}
type Page struct {
	Label      string `json:"label" jsonschema:"minLength=1"`
	Path       string `json:"path" jsonschema:"pattern=^/"`
	Kind       string `json:"kind" jsonschema:"enum=table,enum=custom,enum=system"`
	Endpoint   string `json:"endpoint,omitempty"`
	Permission string `json:"permission,omitempty"`
	Policy     string `json:"policy,omitempty"`
	Order      int    `json:"order,omitempty"`
}
type Comms struct {
	SMTP   *SMTP            `json:"smtp,omitempty"`
	Groups map[string]Group `json:"groups,omitempty"`
}
type SMTP struct {
	Addr        string `json:"addr"`
	From        string `json:"from"`
	Username    string `json:"username,omitempty"`
	PasswordEnv string `json:"password_env,omitempty" jsonschema:"pattern=^[A-Z][A-Z0-9_]*$"`
	TLS         string `json:"tls" jsonschema:"enum=starttls,enum=implicit,enum=none"`
}
type Group struct {
	Recipients []string `json:"recipients" jsonschema:"minItems=1"`
}
type Permission struct {
	Description string `json:"description" jsonschema:"minLength=1"`
}
type Serving struct {
	Addr       string `json:"addr" jsonschema:"minLength=1"`
	PublicURL  string `json:"public_url" jsonschema:"minLength=1"`
	SessionTTL string `json:"session_ttl"`
}
type Deploy struct {
	Backup  *Backup `json:"backup,omitempty"`
	Image   string  `json:"image" jsonschema:"minLength=1"`
	Channel string  `json:"channel" jsonschema:"enum=stable"`
	Edge    string  `json:"edge" jsonschema:"enum=cloudflare"`
}

func Schema() *jsonschema.Schema {
	r := jsonschema.Reflector{Anonymous: true}
	return r.Reflect(&Config{})
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read registry: %w", err)
	}
	c, err := Parse(b)
	if err != nil {
		return nil, err
	}
	if err := c.ValidateFiles(filepath.Dir(path)); err != nil {
		return nil, err
	}
	return c, nil
}

func Parse(b []byte) (*Config, error) {
	var raw any
	d := yaml.NewDecoder(bytes.NewReader(b))
	if err := d.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode registry: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("registry must contain exactly one YAML document")
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("registry must use string keys: %w", err)
	}
	// Validate the same schema published for editors before decoding Go zero values.
	schemaJSON, err := json.Marshal(Schema())
	if err != nil {
		return nil, err
	}
	schemaDoc, err := validator.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return nil, err
	}
	compiler := validator.NewCompiler()
	if err := compiler.AddResource("ddp.json", schemaDoc); err != nil {
		return nil, err
	}
	schema, err := compiler.Compile("ddp.json")
	if err != nil {
		return nil, err
	}
	value, err := validator.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if err := schema.Validate(value); err != nil {
		return nil, fmt.Errorf("invalid registry: %w", err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var relationPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)
var permissionPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)

func (c *Config) Validate() error {
	if c.Deploy.Backup != nil {
		if err := ValidateBackup(c.Deploy.Backup); err != nil {
			return err
		}
	}
	if branding := c.Ddp.Branding; branding != nil {
		if branding.Accent != "" && !regexp.MustCompile(`^#[a-fA-F0-9]{6}$`).MatchString(branding.Accent) {
			return fmt.Errorf("branding accent must be a six-digit hex color")
		}
		if branding.Logo != "" {
			asset, ok := strings.CutPrefix(branding.Logo, "frontend/apps/portal/public/")
			if !ok || asset == "" || path.Clean(asset) != asset || strings.HasPrefix(asset, "/") || !regexp.MustCompile(`^[A-Za-z0-9_./-]+\.(svg|png|jpg|jpeg|webp|gif)$`).MatchString(asset) || slices.Contains(strings.Split(asset, "/"), "..") {
				return fmt.Errorf("branding logo must be an image inside frontend/apps/portal/public")
			}
		}
	}
	policy := c.RetentionPolicy()
	for name, value := range map[string]string{"run_logs": policy.RunLogs, "health": policy.Health, "messages": policy.Messages} {
		if err := positiveDuration("retention."+name, value); err != nil {
			return err
		}
	}

	if c.Comms.SMTP != nil {
		if err := ValidateSMTP(c.Comms.SMTP); err != nil {
			return err
		}
	}
	for name, group := range c.Comms.Groups {
		if len(group.Recipients) < 1 || len(group.Recipients) > 100 {
			return fmt.Errorf("group/%s needs 1-100 recipients", name)
		}
		for _, recipient := range group.Recipients {
			if !ValidEmail(recipient) {
				return fmt.Errorf("group/%s has an invalid recipient", name)
			}
		}
	}

	if _, err := time.LoadLocation(c.Ddp.Timezone); err != nil {
		return fmt.Errorf("ddp.timezone: %w", err)
	}
	if strings.Join(c.Database.Layers, ",") != "staging,core,mart" {
		return fmt.Errorf("database.layers must be [staging, core, mart]")
	}
	for key, value := range map[string]string{"scheduler.defaults.timeout": c.Scheduler.Defaults.Timeout, "serving.session_ttl": c.Serving.SessionTTL} {
		if err := positiveDuration(key, value); err != nil {
			return err
		}
	}
	if err := validateRetry(c.Scheduler.Defaults.Retry); err != nil {
		return err
	}
	refs := c.References()
	for ref := range refs {
		kind, name, _ := strings.Cut(ref, "/")
		valid := namePattern.MatchString(name)
		if kind == "table" || kind == "model" {
			valid = relationPattern.MatchString(name)
		}
		if kind == "permission" {
			valid = permissionPattern.MatchString(name)
		}
		if !valid {
			return fmt.Errorf("invalid registry name %q", ref)
		}
	}
	for ref, deps := range refs {
		for _, dep := range deps {
			if _, ok := refs[dep]; !ok {
				return fmt.Errorf("%s references unknown %q", ref, dep)
			}
		}
	}
	for name, job := range c.Jobs {
		if err := job.ValidateIdempotency(); err != nil {
			return fmt.Errorf("job/%s: %w", name, err)
		}
		if (job.Python == "") == (job.Model == "") {
			return fmt.Errorf("job/%s must have exactly one of python or model", name)
		}
		if job.Model != "" && !strings.HasPrefix(job.Model, "model/") {
			return fmt.Errorf("job/%s requires a model reference", name)
		}
		for _, dep := range job.Reads {
			if !hasKind(dep, "integration", "table", "model") {
				return fmt.Errorf("job/%s reads must reference integrations or relations", name)
			}
		}
		if job.Schedule != "" {
			if _, err := schedule.Parse(job.Schedule, c.Ddp.Timezone); err != nil {
				return fmt.Errorf("job/%s: %w", name, err)
			}
		}
		if job.Schedule != "" && len(job.After) > 0 {
			return fmt.Errorf("job/%s cannot combine schedule and after", name)
		}
		for _, dep := range job.After {
			if !strings.HasPrefix(dep, "job/") {
				return fmt.Errorf("job/%s after must reference jobs", name)
			}
		}
		if job.Action == "ingest" && job.Deletions == "" {
			return fmt.Errorf("job/%s must declare deletions", name)
		}
		if job.Timeout != "" {
			if err := positiveDuration("job/"+name+" timeout", job.Timeout); err != nil {
				return err
			}
		}
		if job.Retry != nil {
			if err := validateRetry(*job.Retry); err != nil {
				return err
			}
		}
		for _, write := range job.Writes {
			if !strings.HasPrefix(write.Target, "table/") {
				return fmt.Errorf("job/%s must write a table reference", name)
			}
			if write.Mode == "upsert" && len(write.Key) == 0 {
				return fmt.Errorf("job/%s upsert requires a key", name)
			}
		}
	}
	for name, model := range c.Models {
		if model.Schedule != "" {
			if _, err := schedule.Parse(model.Schedule, c.Ddp.Timezone); err != nil {
				return fmt.Errorf("model/%s: %w", name, err)
			}
		}
		if _, exists := c.Tables[name]; exists {
			return fmt.Errorf("model/%s and table/%s cannot own the same relation", name, name)
		}
		layer, _, _ := strings.Cut(name, ".")
		rank := map[string]int{"staging": 1, "core": 2, "mart": 3}
		if rank[layer] == 0 {
			return fmt.Errorf("model/%s must be in staging, core or mart", name)
		}
		for _, dep := range model.Reads {
			kind, rel, _ := strings.Cut(dep, "/")
			if kind != "model" && kind != "table" {
				return fmt.Errorf("model/%s must read relations", name)
			}
			schema, _, _ := strings.Cut(rel, ".")
			if rank[schema] >= rank[layer] {
				return fmt.Errorf("model/%s cannot read same or higher layer %s", name, dep)
			}
		}
		if model.Materialization == "materialized_view" && len(model.Contract.UniqueKey) == 0 {
			return fmt.Errorf("model/%s materialized view requires unique_key", name)
		}
	}
	for name, endpoint := range c.Endpoints {
		if err := c.ValidateEndpoint(endpoint); err != nil {
			return fmt.Errorf("endpoint/%s: %w", name, err)
		}
		for _, dep := range endpoint.Reads {
			if !hasKind(dep, "table", "model") {
				return fmt.Errorf("endpoint/%s reads must reference relations", name)
			}
		}
		if endpoint.Policy != "public" && endpoint.Policy != "authenticated" && endpoint.Policy != "admin" && !strings.HasPrefix(endpoint.Policy, "permission:") {
			return fmt.Errorf("endpoint/%s requires public, authenticated, admin or permission:<name> policy", name)
		}
	}
	for name, page := range c.Pages {
		if slices.Contains([]string{"/login", "/welcome", "/forgot-password", "/reset-password", "/profile", "/admin/users"}, page.Path) {
			return fmt.Errorf("page/%s path is reserved for a built-in account page", name)
		}
		if page.Kind == "system" && page.Policy != "admin" {
			return fmt.Errorf("page/%s system page requires admin policy", name)
		}
		if page.Endpoint != "" && !hasKind(page.Endpoint, "endpoint") {
			return fmt.Errorf("page/%s endpoint must reference an endpoint", name)
		}
		if page.Kind == "table" && page.Endpoint == "" {
			return fmt.Errorf("page/%s table page requires an endpoint", name)
		}
		if page.Policy != "" && page.Policy != "admin" {
			return fmt.Errorf("page/%s policy must be admin; use permission for client access", name)
		}
		if page.Policy == "admin" && page.Permission != "" {
			return fmt.Errorf("page/%s must choose admin policy or a client permission", name)
		}
	}
	for name, health := range c.Health {
		if health.Kind != "freshness" && health.Kind != "sql" {
			return fmt.Errorf("health/%s has unknown kind", name)
		}
		for _, target := range health.Notify {
			if !hasKind(target, "group") {
				return fmt.Errorf("health/%s notify must reference groups", name)
			}
		}
		if health.Target != "" && !hasKind(health.Target, "table", "model", "job", "integration") {
			return fmt.Errorf("health/%s target must reference a relation, job or integration", name)
		}
		if health.Kind == "freshness" {
			if health.SQL != "" || health.Target == "" || health.Column == "" {
				return fmt.Errorf("health/%s freshness requires target and column, and forbids sql", name)
			}
			if !hasKind(health.Target, "table", "model") {
				return fmt.Errorf("health/%s freshness target must reference a table or model", name)
			}
			if err := positiveDuration("health."+name+".max_age", health.MaxAge); err != nil {
				return err
			}
			kind, relName, ok := strings.Cut(health.Target, "/")
			if !ok {
				return fmt.Errorf("health/%s has invalid target", name)
			}
			var contract Contract
			if kind == "table" {
				contract = c.Tables[relName].Contract
			} else {
				contract = c.Models[relName].Contract
			}
			column, exists := contract.Columns[health.Column]
			if !exists || (column.Type != "timestamp" && column.Type != "timestamptz") {
				return fmt.Errorf("health/%s column must be a declared timestamp column", name)
			}
		} else {
			if health.SQL == "" || health.Column != "" || health.MaxAge != "" {
				return fmt.Errorf("health/%s sql requires sql and forbids freshness fields", name)
			}
			if health.Target != "" && !hasKind(health.Target, "table", "model", "job", "integration") {
				return fmt.Errorf("health/%s target must reference a relation, job or integration", name)
			}
		}
	}
	return checkCycles(refs)
}

// ValidateEndpoint checks the declared query surface against its relation contract.
func (c *Config) ValidateEndpoint(e Endpoint) error {
	if len(e.Reads) != 1 || len(e.Columns) == 0 {
		return fmt.Errorf("needs one relation and columns")
	}
	if !strings.HasPrefix(e.Path, "/api/") || e.Path == "/api/" || path.Clean(e.Path) != e.Path || strings.ContainsAny(e.Path, "{}?%# \t\r\n") {
		return fmt.Errorf("path must be a literal path below /api/")
	}
	kind, name, _ := strings.Cut(e.Reads[0], "/")
	var contract Contract
	switch kind {
	case "table":
		contract = c.Tables[name].Contract
	case "model":
		contract = c.Models[name].Contract
	default:
		return fmt.Errorf("reads must name a table or model")
	}
	if len(contract.Columns) == 0 {
		return fmt.Errorf("relation contract is missing")
	}
	if e.Shape != "" && e.Shape != "singleton" {
		return fmt.Errorf("unknown shape")
	}
	if e.PageSize < 0 || e.PageSize > 1000 {
		return fmt.Errorf("page_size out of range")
	}
	if e.Export != nil && (e.Export.Format != "csv" || e.Export.MaxRows < 1 || e.Export.MaxRows == int(^uint(0)>>1)) {
		return fmt.Errorf("export requires csv and a positive bounded max_rows")
	}
	for _, declaration := range []struct {
		name    string
		columns []string
	}{
		{"columns", e.Columns}, {"filters", e.Filters}, {"sort", e.Sort}, {"search", e.Search}, {"unique_key", e.UniqueKey},
	} {
		field, names := declaration.name, declaration.columns
		seen := map[string]bool{}
		for _, name := range names {
			if field == "sort" {
				name = strings.TrimPrefix(name, "-")
			}
			if seen[name] {
				return fmt.Errorf("%s repeats column %s", field, name)
			}
			seen[name] = true
			if _, ok := contract.Columns[name]; !ok {
				return fmt.Errorf("%s references unknown column %s", field, name)
			}
			if field != "columns" && !slices.Contains(e.Columns, name) {
				return fmt.Errorf("%s column %s must be returned", field, name)
			}
		}
	}
	if e.Shape == "singleton" {
		if len(e.UniqueKey) > 0 || len(e.Sort) > 0 || e.PageSize != 0 {
			return fmt.Errorf("singleton cannot declare pagination or sort")
		}
		return nil
	}
	if len(e.UniqueKey) == 0 {
		return fmt.Errorf("needs unique_key")
	}
	for _, name := range e.UniqueKey {
		if contract.Columns[name].Nullable {
			return fmt.Errorf("unique_key column %s must be non-nullable", name)
		}
	}
	for _, key := range [][]string{contract.PrimaryKey, contract.UniqueKey} {
		if len(key) > 0 && !slices.ContainsFunc(key, func(name string) bool { return !slices.Contains(e.UniqueKey, name) }) {
			return nil
		}
	}
	return fmt.Errorf("unique_key must include a primary_key or unique_key from the relation contract")
}

func hasKind(ref string, allowed ...string) bool {
	kind, _, ok := strings.Cut(ref, "/")
	return ok && slices.Contains(allowed, kind)
}

// References returns each declared resource and its forward dependencies.
func (c *Config) References() map[string][]string {
	refs := map[string][]string{}
	for name := range c.Integrations {
		refs["integration/"+name] = nil
	}
	for name := range c.Tables {
		refs["table/"+name] = nil
	}
	for name := range c.Permissions {
		refs["permission/"+name] = nil
	}
	for name := range c.Comms.Groups {
		refs["group/"+name] = nil
	}
	for name := range c.Jobs {
		refs["job/"+name] = nil
	}
	for name := range c.Models {
		refs["model/"+name] = nil
	}
	for name := range c.Endpoints {
		refs["endpoint/"+name] = nil
	}
	for name := range c.Pages {
		refs["page/"+name] = nil
	}
	for name := range c.Health {
		refs["health/"+name] = nil
	}
	for _, link := range c.Relationships() {
		refs[link.From] = append(refs[link.From], link.To)
	}
	for ref, deps := range refs {
		slices.Sort(deps)
		refs[ref] = slices.Compact(deps)
	}
	return refs
}

// Relationship retains why a resource names another resource. The owner is From;
// for example a job's writes edge points to its output table.
type Relationship struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

func (c *Config) Relationships() []Relationship {
	links := []Relationship{}
	add := func(from, kind string, targets ...string) {
		for _, target := range targets {
			if target != "" {
				links = append(links, Relationship{From: from, To: target, Kind: kind})
			}
		}
	}
	for name, job := range c.Jobs {
		ref := "job/" + name
		add(ref, "reads", job.Reads...)
		add(ref, "after", job.After...)
		add(ref, "model", job.Model)
		for _, write := range job.Writes {
			add(ref, "writes", write.Target)
		}
	}
	for name, model := range c.Models {
		add("model/"+name, "reads", model.Reads...)
	}
	for name, endpoint := range c.Endpoints {
		ref := "endpoint/" + name
		add(ref, "reads", endpoint.Reads...)
		if permission, ok := strings.CutPrefix(endpoint.Policy, "permission:"); ok {
			add(ref, "permission", "permission/"+permission)
		}
	}
	for name, page := range c.Pages {
		ref := "page/" + name
		add(ref, "endpoint", page.Endpoint)
		if page.Permission != "" {
			add(ref, "permission", "permission/"+page.Permission)
		}
	}
	for name, health := range c.Health {
		ref := "health/" + name
		add(ref, "target", health.Target)
		add(ref, "notify", health.Notify...)
	}
	slices.SortFunc(links, func(a, b Relationship) int {
		return cmp.Or(strings.Compare(a.From, b.From), strings.Compare(a.Kind, b.Kind), strings.Compare(a.To, b.To))
	})
	return slices.Compact(links)
}

func checkCycles(refs map[string][]string) error {
	state := map[string]int{}
	var visit func(string) error
	visit = func(ref string) error {
		if state[ref] == 1 {
			return fmt.Errorf("registry dependency cycle at %s", ref)
		}
		if state[ref] == 2 {
			return nil
		}
		state[ref] = 1
		for _, dep := range refs[ref] {
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[ref] = 2
		return nil
	}
	for ref := range refs {
		if err := visit(ref); err != nil {
			return err
		}
	}
	return nil
}

func positiveDuration(name, value string) error {
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return fmt.Errorf("%s must be a positive duration", name)
	}
	return nil
}
func validateRetry(r Retry) error {
	if r.MaxAttempts < 1 {
		return fmt.Errorf("retry.max_attempts must be positive")
	}
	if err := positiveDuration("retry.initial_delay", r.InitialDelay); err != nil {
		return err
	}
	if err := positiveDuration("retry.max_delay", r.MaxDelay); err != nil {
		return err
	}
	a, _ := time.ParseDuration(r.InitialDelay)
	b, _ := time.ParseDuration(r.MaxDelay)
	if a > b {
		return fmt.Errorf("retry.max_delay must be at least initial_delay")
	}
	return nil
}

func (c *Config) ValidateFiles(root string) error {
	paths := []string{}
	for _, job := range c.Jobs {
		if job.Python != "" {
			paths = append(paths, strings.ReplaceAll(job.Python, ".", "/")+".py")
		}
	}
	for _, model := range c.Models {
		paths = append(paths, model.File)
	}
	for _, health := range c.Health {
		if health.SQL != "" {
			paths = append(paths, health.SQL)
		}
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer dir.Close()
	for _, path := range paths {
		if !filepath.IsLocal(path) {
			return fmt.Errorf("entrypoint must be inside project: %s", path)
		}
		info, err := dir.Stat(path)
		if err != nil {
			return fmt.Errorf("entrypoint %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("entrypoint is not a regular file: %s", path)
		}
	}
	return nil
}

func (j Job) SideEffecting() bool {
	return j.Action == "notify" || j.Action == "export" || j.Action == "operate"
}

func (j Job) ValidateIdempotency() error {
	if j.Idempotency == nil {
		if j.SideEffecting() {
			return fmt.Errorf("%s requires an idempotency strategy", j.Action)
		}
		return nil
	}
	switch j.Idempotency.Strategy {
	case "provider_key", "natural_key", "reconcile":
	case "duplicates_acceptable":
		if strings.TrimSpace(j.Idempotency.Reason) == "" {
			return fmt.Errorf("duplicates_acceptable requires a reason")
		}
	default:
		return fmt.Errorf("unknown idempotency strategy %q", j.Idempotency.Strategy)
	}
	return nil
}

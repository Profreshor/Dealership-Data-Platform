package onboard

import (
	"errors"
	"fmt"
	"io"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type Discovery struct {
	Client         Client         `yaml:"client" json:"client"`
	Users          []User         `yaml:"users" json:"users"`
	Systems        []System       `yaml:"systems" json:"systems"`
	Questions      []string       `yaml:"questions" json:"questions"`
	Outputs        Outputs        `yaml:"outputs" json:"outputs"`
	Communications Communications `yaml:"communications" json:"communications"`
	Hosting        Hosting        `yaml:"hosting" json:"hosting"`
	Ownership      Ownership      `yaml:"ownership" json:"ownership"`
	Recovery       Recovery       `yaml:"recovery" json:"recovery"`
	Constraints    Constraints    `yaml:"constraints" json:"constraints"`
	Success30Days  []string       `yaml:"success_30_days" json:"success_30_days"`
}
type Client struct {
	Name      string    `yaml:"name" json:"name"`
	Industry  string    `yaml:"industry" json:"industry"`
	Timezone  string    `yaml:"timezone" json:"timezone"`
	Locations []string  `yaml:"locations" json:"locations"`
	Contacts  []Contact `yaml:"contacts" json:"contacts"`
}
type Contact struct {
	Name  string `yaml:"name" json:"name"`
	Email string `yaml:"email" json:"email"`
}
type User struct {
	Name  string `yaml:"name" json:"name"`
	Email string `yaml:"email" json:"email"`
	Role  string `yaml:"role" json:"role"`
}
type System struct {
	Name            string   `yaml:"name" json:"name"`
	Kind            string   `yaml:"kind" json:"kind"`
	Vendor          string   `yaml:"vendor" json:"vendor"`
	Access          string   `yaml:"access" json:"access"`
	DocsURL         string   `yaml:"docs_url" json:"docs_url"`
	CredentialOwner string   `yaml:"credential_owner" json:"credential_owner"`
	Entities        []Entity `yaml:"entities" json:"entities"`
}
type Entity struct {
	Name             string `yaml:"name" json:"name"`
	DeletionBehavior string `yaml:"deletion_behavior" json:"deletion_behavior"`
}
type Outputs struct {
	Pages  []string `yaml:"pages" json:"pages"`
	Emails []Email  `yaml:"emails" json:"emails"`
}
type Email struct {
	Name       string   `yaml:"name" json:"name"`
	Cadence    string   `yaml:"cadence" json:"cadence"`
	Recipients []string `yaml:"recipients" json:"recipients"`
}
type Communications struct {
	Groups []Group `yaml:"groups" json:"groups"`
}
type Group struct {
	Name       string   `yaml:"name" json:"name"`
	Recipients []string `yaml:"recipients" json:"recipients"`
}
type Hosting struct {
	Mode       string `yaml:"mode" json:"mode"`
	Domain     string `yaml:"domain" json:"domain"`
	Exposure   string `yaml:"exposure" json:"exposure"`
	Repository string `yaml:"repository" json:"repository"`
}
type Ownership struct {
	Repository             string `yaml:"repository" json:"repository"`
	Host                   string `yaml:"host" json:"host"`
	Domain                 string `yaml:"domain" json:"domain"`
	Cloudflare             string `yaml:"cloudflare" json:"cloudflare"`
	Backups                string `yaml:"backups" json:"backups"`
	IntegrationCredentials string `yaml:"integration_credentials" json:"integration_credentials"`
}
type Recovery struct {
	BackupInterval    string `yaml:"backup_interval" json:"backup_interval"`
	BackupRetention   string `yaml:"backup_retention" json:"backup_retention"`
	MaxDataLoss       string `yaml:"max_data_loss" json:"max_data_loss"`
	TargetRestoreTime string `yaml:"target_restore_time" json:"target_restore_time"`
}
type Constraints struct {
	PII             []string `yaml:"pii" json:"pii"`
	DataRetention   string   `yaml:"data_retention" json:"data_retention"`
	DevelopmentData string   `yaml:"development_data" json:"development_data"`
}

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
var reserved = map[string]bool{"app": true, "ddp": true, "ops": true, "staging": true, "core": true, "mart": true, "public": true, "information_schema": true}

func ParseDiscovery(data []byte) (Discovery, error) {
	if len(data) > 1<<20 {
		return Discovery{}, errors.New("discovery YAML exceeds 1 MiB")
	}
	var node yaml.Node
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(&node); err != nil {
		return Discovery{}, fmt.Errorf("invalid discovery YAML: %w", err)
	}
	if hasAlias(&node) {
		return Discovery{}, errors.New("discovery YAML aliases are not allowed")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != nil && !errors.Is(err, io.EOF) {
		return Discovery{}, errors.New("discovery YAML must contain exactly one document")
	} else if err == nil {
		return Discovery{}, errors.New("discovery YAML must contain exactly one document")
	}
	var discovery Discovery
	strict := yaml.NewDecoder(strings.NewReader(string(data)))
	strict.KnownFields(true)
	if err := strict.Decode(&discovery); err != nil {
		return Discovery{}, fmt.Errorf("invalid discovery fields: %w", err)
	}
	if len(node.Content) == 0 || node.Content[0].Kind != yaml.MappingNode {
		return Discovery{}, errors.New("discovery document must be a mapping")
	}
	if err := requiredSequences(node.Content[0]); err != nil {
		return Discovery{}, err
	}
	if err := validateDiscovery(discovery); err != nil {
		return Discovery{}, err
	}
	return discovery, nil
}

func hasAlias(node *yaml.Node) bool {
	if node.Kind == yaml.AliasNode {
		return true
	}
	for _, child := range node.Content {
		if hasAlias(child) {
			return true
		}
	}
	return false
}
func required(value, field string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	return nil
}
func validEmail(value, field string) error {
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address != value || !strings.Contains(value, "@") {
		return fmt.Errorf("%s must be a valid email", field)
	}
	return nil
}
func validIdentifier(value, field string) error {
	if !identifier.MatchString(value) {
		return fmt.Errorf("%s must be a lowercase identifier", field)
	}
	return nil
}
func duration(value, field string) (time.Duration, error) {
	if err := required(value, field); err != nil {
		return 0, err
	}
	if strings.HasSuffix(value, "d") {
		days, err := strconv.ParseInt(strings.TrimSuffix(value, "d"), 10, 64)
		if err != nil || days <= 0 || days > int64(time.Duration(1<<63-1)/(24*time.Hour)) {
			return 0, fmt.Errorf("%s must be a positive duration", field)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", field)
	}
	return parsed, nil
}
func ownership(value, field string) error {
	if value != "operator" && value != "dealership" {
		return fmt.Errorf("%s must be operator or dealership", field)
	}
	return nil
}

func mapping(node *yaml.Node) map[string]*yaml.Node {
	out := map[string]*yaml.Node{}
	if node.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		out[node.Content[i].Value] = node.Content[i+1]
	}
	return out
}
func requiredSequences(root *yaml.Node) error {
	m := mapping(root)
	for _, key := range []string{"client", "users", "systems", "questions", "outputs", "communications", "hosting", "ownership", "recovery", "constraints", "success_30_days"} {
		if _, ok := m[key]; !ok {
			return fmt.Errorf("%s is required", key)
		}
	}
	client := mapping(m["client"])
	for _, key := range []string{"name", "industry", "timezone", "locations", "contacts"} {
		if _, ok := client[key]; !ok {
			return fmt.Errorf("client.%s is required", key)
		}
	}
	outputs := mapping(m["outputs"])
	for _, key := range []string{"pages", "emails"} {
		if _, ok := outputs[key]; !ok {
			return fmt.Errorf("outputs.%s is required", key)
		}
	}
	comms := mapping(m["communications"])
	if _, ok := comms["groups"]; !ok {
		return errors.New("communications.groups is required")
	}
	constraints := mapping(m["constraints"])
	if _, ok := constraints["pii"]; !ok {
		return errors.New("constraints.pii is required")
	}
	for path, node := range map[string]*yaml.Node{"users": m["users"], "systems": m["systems"], "questions": m["questions"], "success_30_days": m["success_30_days"], "client.locations": client["locations"], "client.contacts": client["contacts"], "outputs.pages": outputs["pages"], "outputs.emails": outputs["emails"], "communications.groups": comms["groups"], "constraints.pii": constraints["pii"]} {
		if node == nil || node.Kind != yaml.SequenceNode {
			return fmt.Errorf("%s must be an explicit sequence", path)
		}
	}
	for i, system := range m["systems"].Content {
		entities := mapping(system)["entities"]
		if entities == nil || entities.Kind != yaml.SequenceNode {
			return fmt.Errorf("systems[%d].entities must be an explicit sequence", i)
		}
	}
	return nil
}

func validateDiscovery(d Discovery) error {
	for _, pair := range []struct{ v, n string }{{d.Client.Name, "client.name"}, {d.Client.Industry, "client.industry"}, {d.Client.Timezone, "client.timezone"}, {d.Hosting.Mode, "hosting.mode"}, {d.Hosting.Domain, "hosting.domain"}, {d.Hosting.Exposure, "hosting.exposure"}, {d.Hosting.Repository, "hosting.repository"}, {d.Ownership.Repository, "ownership.repository"}, {d.Recovery.BackupInterval, "recovery.backup_interval"}, {d.Recovery.BackupRetention, "recovery.backup_retention"}, {d.Recovery.MaxDataLoss, "recovery.max_data_loss"}, {d.Recovery.TargetRestoreTime, "recovery.target_restore_time"}, {d.Constraints.DataRetention, "constraints.data_retention"}, {d.Constraints.DevelopmentData, "constraints.development_data"}} {
		if err := required(pair.v, pair.n); err != nil {
			return err
		}
	}
	if _, err := time.LoadLocation(d.Client.Timezone); err != nil {
		return fmt.Errorf("client.timezone is invalid")
	}
	if len(d.Users) == 0 || len(d.Client.Contacts) == 0 || len(d.Questions) == 0 || len(d.Success30Days) == 0 {
		return errors.New("users, contacts, questions, and success_30_days require at least one item")
	}
	for i, question := range d.Questions {
		if err := required(question, fmt.Sprintf("questions[%d]", i)); err != nil {
			return err
		}
	}
	for i, success := range d.Success30Days {
		if err := required(success, fmt.Sprintf("success_30_days[%d]", i)); err != nil {
			return err
		}
	}
	if d.Hosting.Mode != "self" && d.Hosting.Mode != "cloud" {
		return errors.New("hosting.mode must be self or cloud")
	}
	if d.Hosting.Exposure != "cloudflare" {
		return errors.New("hosting.exposure must be cloudflare")
	}
	if len(d.Hosting.Domain) > 253 || !strings.Contains(d.Hosting.Domain, ".") {
		return errors.New("hosting.domain must be a DNS name")
	}
	for _, label := range strings.Split(d.Hosting.Domain, ".") {
		if !regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`).MatchString(label) {
			return errors.New("hosting.domain must be a DNS name")
		}
	}
	repo, err := url.Parse(d.Hosting.Repository)
	if err != nil || repo.Scheme != "https" || repo.Host != "github.com" || repo.User != nil || repo.RawQuery != "" || repo.Fragment != "" || repo.RawPath != "" || !regexp.MustCompile(`^/[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9_][A-Za-z0-9_.-]*$`).MatchString(repo.Path) || strings.HasSuffix(repo.Path, ".git") {
		return errors.New("hosting.repository must be an https://github.com/org/repo URL without credentials or .git suffix")
	}
	emails := map[string]bool{}
	for i, user := range d.Users {
		if err := required(user.Name, fmt.Sprintf("users[%d].name", i)); err != nil {
			return err
		}
		if err := validEmail(user.Email, fmt.Sprintf("users[%d].email", i)); err != nil {
			return err
		}
		if emails[strings.ToLower(user.Email)] {
			return fmt.Errorf("duplicate user email at users[%d].email", i)
		}
		emails[strings.ToLower(user.Email)] = true
		if err := validIdentifier(user.Role, fmt.Sprintf("users[%d].role", i)); err != nil {
			return err
		}
	}
	for i, contact := range d.Client.Contacts {
		if err := required(contact.Name, fmt.Sprintf("client.contacts[%d].name", i)); err != nil {
			return err
		}
		if err := validEmail(contact.Email, fmt.Sprintf("client.contacts[%d].email", i)); err != nil {
			return err
		}
	}
	systemNames := map[string]string{}
	emailNames := map[string]string{}
	groupNames := map[string]string{}
	for i, system := range d.Systems {
		if err := required(system.Vendor, fmt.Sprintf("systems[%d].vendor", i)); err != nil {
			return err
		}
		u, urlErr := url.Parse(system.DocsURL)
		if urlErr != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
			return fmt.Errorf("systems[%d].docs_url must be an HTTP documentation URL without credentials", i)
		}
		if err := required(system.DocsURL, fmt.Sprintf("systems[%d].docs_url", i)); err != nil {
			return err
		}
		if err := required(system.CredentialOwner, fmt.Sprintf("systems[%d].credential_owner", i)); err != nil {
			return err
		}
		if err := validIdentifier(system.Name, fmt.Sprintf("systems[%d].name", i)); err != nil {
			return err
		}
		if reserved[system.Name] || strings.HasPrefix(system.Name, "pg_") {
			return fmt.Errorf("systems[%d].name is a reserved schema", i)
		}
		if prior := systemNames[system.Name]; prior != "" {
			return fmt.Errorf("duplicate system identifier %q", system.Name)
		}
		systemNames[system.Name] = system.Name
		if system.Kind != "dms" && system.Kind != "erp" && system.Kind != "accounting" && system.Kind != "crm" && system.Kind != "scheduling" && system.Kind != "phone" && system.Kind != "spreadsheet" && system.Kind != "other" {
			return fmt.Errorf("systems[%d].kind is invalid", i)
		}
		if system.Access != "api" && system.Access != "database" && system.Access != "export" && system.Access != "scrape" && system.Access != "none" {
			return fmt.Errorf("systems[%d].access is invalid", i)
		}
		if err := ownership(system.CredentialOwner, fmt.Sprintf("systems[%d].credential_owner", i)); err != nil {
			return err
		}
		entities := map[string]bool{}
		for j, entity := range system.Entities {
			if err := validIdentifier(entity.Name, fmt.Sprintf("systems[%d].entities[%d].name", i, j)); err != nil {
				return err
			}
			if entities[entity.Name] {
				return fmt.Errorf("duplicate entity identifier %q", entity.Name)
			}
			entities[entity.Name] = true
			if entity.DeletionBehavior != "ignore" && entity.DeletionBehavior != "tombstone" && entity.DeletionBehavior != "reconcile" && entity.DeletionBehavior != "replace" {
				return fmt.Errorf("systems[%d].entities[%d].deletion_behavior is invalid", i, j)
			}
		}
	}
	for _, text := range append(append(slices.Clone(d.Client.Locations), d.Outputs.Pages...), d.Constraints.PII...) {
		if strings.TrimSpace(text) == "" {
			return errors.New("discovery lists must not contain blank items")
		}
	}
	for i, email := range d.Outputs.Emails {
		if err := validIdentifier(email.Name, fmt.Sprintf("outputs.emails[%d].name", i)); err != nil {
			return err
		}
		if emailNames[email.Name] != "" {
			return fmt.Errorf("duplicate email identifier %q", email.Name)
		}
		emailNames[email.Name] = email.Name
		if err := required(email.Cadence, "outputs email cadence"); err != nil {
			return err
		}
		if len(email.Recipients) == 0 {
			return errors.New("email recipients are required")
		}
		for _, recipient := range email.Recipients {
			if err := validEmail(recipient, "outputs email recipient"); err != nil {
				return err
			}
		}
	}
	for i, group := range d.Communications.Groups {
		if err := validIdentifier(group.Name, fmt.Sprintf("communications.groups[%d].name", i)); err != nil {
			return err
		}
		if groupNames[group.Name] != "" {
			return fmt.Errorf("duplicate group identifier %q", group.Name)
		}
		groupNames[group.Name] = group.Name
		if len(group.Recipients) == 0 {
			return errors.New("group recipients are required")
		}
		for _, recipient := range group.Recipients {
			if err := validEmail(recipient, "group recipient"); err != nil {
				return err
			}
		}
	}
	for _, field := range []struct{ v, n string }{{d.Ownership.Repository, "ownership.repository"}, {d.Ownership.Host, "ownership.host"}, {d.Ownership.Domain, "ownership.domain"}, {d.Ownership.Cloudflare, "ownership.cloudflare"}, {d.Ownership.Backups, "ownership.backups"}, {d.Ownership.IntegrationCredentials, "ownership.integration_credentials"}} {
		if err := ownership(field.v, field.n); err != nil {
			return err
		}
	}
	interval, err := duration(d.Recovery.BackupInterval, "recovery.backup_interval")
	if err != nil {
		return err
	}
	retention, err := duration(d.Recovery.BackupRetention, "recovery.backup_retention")
	if err != nil || retention < interval {
		return fmt.Errorf("recovery.backup_retention must be at least backup_interval")
	}
	loss, err := duration(d.Recovery.MaxDataLoss, "recovery.max_data_loss")
	if err != nil || loss < interval {
		return fmt.Errorf("recovery.max_data_loss must be at least backup_interval")
	}
	if _, err := duration(d.Recovery.TargetRestoreTime, "recovery.target_restore_time"); err != nil {
		return err
	}
	if d.Constraints.DevelopmentData != "synthetic" {
		return errors.New("constraints.development_data must be synthetic")
	}
	return nil
}

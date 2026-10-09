package backup

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"filippo.io/age"
)

type archiveReader struct {
	ctx context.Context
	io.Reader
}

func (r archiveReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

// connectionURL admits only options that cannot redirect the chosen database.
func connectionURL(raw, database string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" || u.User == nil || u.User.Username() == "" || u.Fragment != "" || strings.TrimPrefix(u.Path, "/") == "" {
		return nil, errors.New("backup requires a PostgreSQL URL with host, user and database")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, errors.New("invalid backup database options")
	}
	for key, values := range q {
		switch key {
		case "sslmode", "sslrootcert", "sslcert", "sslkey", "connect_timeout":
			if len(values) != 1 {
				return nil, errors.New("duplicate backup database option")
			}
		default:
			return nil, errors.New("backup database connection overrides are unsupported")
		}
	}
	ip := net.ParseIP(u.Hostname())
	local := u.Hostname() == "localhost" || u.Hostname() == "postgres" || (ip != nil && ip.IsLoopback())
	if !local && q.Get("sslmode") != "verify-full" {
		return nil, errors.New("external backup database connections require sslmode=verify-full")
	}
	if database != "" {
		u.Path = "/" + database
		u.RawPath = ""
	}
	q.Set("connect_timeout", "10")
	u.RawQuery = q.Encode()
	return u, nil
}

func pgCommand(ctx context.Context, program string, u *url.URL, args ...string) error {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LANG=C", "PGCONNECT_TIMEOUT=10", "PGAPPNAME=ddp-backup"}
	if u != nil {
		password, _ := u.User.Password()
		cmd.Env = append(cmd.Env, "PGHOST="+u.Hostname(), "PGPORT="+u.Port(), "PGUSER="+u.User.Username(), "PGPASSWORD="+password, "PGDATABASE="+strings.TrimPrefix(u.Path, "/"))
		for key, values := range u.Query() {
			cmd.Env = append(cmd.Env, "PG"+strings.ToUpper(key)+"="+values[0])
		}
	}
	// PostgreSQL diagnostics may contain credentials, SQL or business data.
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%s failed; check tool availability, database access and archive compatibility", program)
	}
	return nil
}

func dumpArchive(ctx context.Context, u *url.URL, dir, recipient string) (file *os.File, checksum string, size int64, err error) {
	plain := filepath.Join(dir, "database.dump")
	if err = pgCommand(ctx, "pg_dump", u, "--no-password", "--format=custom", "--file="+plain); err != nil {
		return
	}
	// Read every archived data block, not just the table of contents.
	if err = pgCommand(ctx, "pg_restore", nil, "--file="+os.DevNull, plain); err != nil {
		return
	}
	source, e := os.Open(plain)
	if e != nil {
		err = errors.New("open backup archive")
		return
	}
	defer source.Close()
	key, e := age.ParseX25519Recipient(recipient)
	if e != nil {
		err = errors.New("invalid backup encryption recipient")
		return
	}
	file, e = os.OpenFile(filepath.Join(dir, "database.dump.age"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		err = errors.New("create encrypted backup")
		return
	}
	defer func() {
		if err != nil {
			_ = file.Close()
		}
	}()
	h := sha256.New()
	encrypted, e := age.Encrypt(io.MultiWriter(file, h), key)
	if e != nil {
		err = errors.New("initialize backup encryption")
		return
	}
	if _, e = io.Copy(encrypted, archiveReader{ctx, source}); e != nil {
		err = errors.Join(ctx.Err(), errors.New("encrypt backup"))
		return
	}
	if e = encrypted.Close(); e != nil {
		err = errors.New("finish backup encryption")
		return
	}
	info, e := file.Stat()
	if e != nil {
		err = errors.New("inspect encrypted backup")
		return
	}
	size = info.Size()
	checksum = fmt.Sprintf("%x", h.Sum(nil))
	if _, e = file.Seek(0, io.SeekStart); e != nil {
		err = errors.New("rewind encrypted backup")
		return
	}
	return
}

func downloadArchive(ctx context.Context, s *store, m Manifest, dir string) (string, error) {
	identity, err := age.ParseX25519Identity(os.Getenv("BACKUP_AGE_IDENTITY"))
	if err != nil {
		return "", errors.New("BACKUP_AGE_IDENTITY must contain the backup decryption key")
	}
	body, err := s.get(ctx, m.objectKey())
	if err != nil {
		return "", err
	}
	defer body.Close()
	encrypted, err := os.OpenFile(filepath.Join(dir, "download.age"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", errors.New("create backup download")
	}
	defer encrypted.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(encrypted, h), io.LimitReader(body, m.Bytes+1))
	if err != nil || n != m.Bytes || fmt.Sprintf("%x", h.Sum(nil)) != m.SHA256 {
		return "", errors.New("backup download integrity check failed")
	}
	if _, err = encrypted.Seek(0, io.SeekStart); err != nil {
		return "", errors.New("rewind backup download")
	}
	decrypted, err := age.Decrypt(encrypted, identity)
	if err != nil {
		return "", errors.New("backup decryption failed")
	}
	name := filepath.Join(dir, "restore.dump")
	plain, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", errors.New("create restore archive")
	}
	_, copyErr := io.Copy(plain, archiveReader{ctx, decrypted})
	closeErr := plain.Close()
	if copyErr != nil || closeErr != nil {
		return "", errors.Join(ctx.Err(), errors.New("backup decryption integrity check failed"))
	}
	if err = pgCommand(ctx, "pg_restore", nil, "--file="+os.DevNull, name); err != nil {
		return "", err
	}
	return name, nil
}

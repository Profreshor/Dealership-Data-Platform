package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestStoreRoundTripAndKeys(t *testing.T) {
	var object []byte
	var parts, initiates, completes int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/redirect") {
			http.Redirect(w, r, "https://secret.example/next", http.StatusTemporaryRedirect)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/fail") {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("secret remote URL https://secret.example"))
			return
		}
		if r.URL.Query().Get("list-type") == "2" {
			if r.URL.Query().Get("continuation-token") == "token" {
				_, _ = w.Write([]byte(`<?xml version="1.0"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><IsTruncated>false</IsTruncated><Contents><Key>b</Key></Contents></ListBucketResult>`))
			} else {
				_, _ = w.Write([]byte(`<?xml version="1.0"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><IsTruncated>true</IsTruncated><NextContinuationToken>token</NextContinuationToken><Contents><Key>a</Key></Contents></ListBucketResult>`))
			}
			return
		}
		switch r.Method {
		case http.MethodPost:
			if r.URL.Query().Has("uploads") {
				initiates++
				_, _ = w.Write([]byte(`<InitiateMultipartUploadResult><UploadId>id</UploadId></InitiateMultipartUploadResult>`))
				return
			}
			if r.URL.Query().Get("uploadId") == "id" {
				completes++
			}
			w.WriteHeader(http.StatusOK)
		case http.MethodPut:
			var err error
			part, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if r.URL.Query().Get("partNumber") == "" {
				object = part
			} else {
				parts++
				object = append(object, part...)
			}
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			_, _ = w.Write(object)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	httpClient := srv.Client()
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	cfg := aws.Config{Region: "test", Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "id", SecretAccessKey: "secret"}, nil
	}), HTTPClient: httpClient}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(srv.URL)
		o.UsePathStyle = true
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	})
	s := &store{client: client, transfer: transfermanager.New(client, func(o *transfermanager.Options) {
		o.PartSizeBytes = 8 << 20
		o.MultipartUploadThreshold = 8 << 20
		o.Concurrency = 1
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	}), bucket: "bucket"}
	payload := bytes.Repeat([]byte("data"), (9<<20)/4+1)
	if err := s.put(context.Background(), "a", bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	if initiates != 1 || parts < 2 || completes != 1 {
		t.Fatalf("multipart initiate=%d parts=%d complete=%d", initiates, parts, completes)
	}
	r, err := s.get(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	_ = r.Close()
	if !bytes.Equal(got, payload) {
		t.Fatalf("got %d bytes, prefix %q suffix %q", len(got), got[:min(32, len(got))], got[max(0, len(got)-32):])
	}
	keys, err := s.keys(context.Background(), "")
	if err != nil || len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
		t.Fatalf("keys=%v err=%v", keys, err)
	}
	if err := s.delete(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.get(context.Background(), "fail"); err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "https://") {
		t.Fatalf("unsanitized failure: %v", err)
	}
	if _, err := s.get(context.Background(), "redirect"); err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "https://") {
		t.Fatalf("redirect was followed or leaked: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.get(ctx, "a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error: %v", err)
	}
}

func TestNewStoreRejectsUnsafeEndpointAndMissingCredentials(t *testing.T) {
	for _, endpoint := range []string{"http://example.com", "https://example.com/path", "https://user@example.com", "https://example.com/?x=1"} {
		if _, err := newStore(configForTest(endpoint)); err == nil {
			t.Fatalf("accepted %s", endpoint)
		}
	}
	t.Setenv("BACKUP_ACCESS_KEY_ID", "")
	t.Setenv("BACKUP_SECRET_ACCESS_KEY", "secret")
	if _, err := newStore(configForTest("https://example.com")); err == nil {
		t.Fatal("accepted missing credentials")
	}
}

func configForTest(endpoint string) config.Backup {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		panic(err)
	}
	return config.Backup{Endpoint: endpoint, Region: "test", Bucket: "bucket", Recipient: identity.Recipient().String()}
}

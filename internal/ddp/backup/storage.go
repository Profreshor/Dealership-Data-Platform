package backup

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type store struct {
	client   *s3.Client
	transfer *transfermanager.Client
	bucket   string
}

func newStore(settings config.Backup) (*store, error) {
	if err := config.ValidateBackup(&settings); err != nil {
		return nil, err
	}
	u, err := url.Parse(settings.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("backup endpoint must be an HTTPS origin")
	}
	access, ok := os.LookupEnv("BACKUP_ACCESS_KEY_ID")
	if !ok || access == "" {
		return nil, errors.New("backup credentials are not configured")
	}
	secret, ok := os.LookupEnv("BACKUP_SECRET_ACCESS_KEY")
	if !ok || secret == "" {
		return nil, errors.New("backup credentials are not configured")
	}
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	httpClient := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	provider := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: access, SecretAccessKey: secret}, nil
	})
	client := s3.New(s3.Options{Region: settings.Region, Credentials: provider, HTTPClient: httpClient, BaseEndpoint: aws.String(settings.Endpoint), UsePathStyle: true, RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired, ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired})
	return &store{client: client, transfer: transfermanager.New(client, func(o *transfermanager.Options) {
		o.PartSizeBytes = 8 << 20
		o.MultipartUploadThreshold = 8 << 20
		o.Concurrency = 1
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	}), bucket: settings.Bucket}, nil
}

func (s *store) put(ctx context.Context, key string, body io.Reader) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("backup key is required")
	}
	_, err := s.transfer.UploadObject(ctx, &transfermanager.UploadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: body})
	return storageError(ctx, err)
}

func (s *store) get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, storageError(ctx, err)
	}
	return out.Body, nil
}

func (s *store) keys(ctx context.Context, prefix string) ([]string, error) {
	p := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(prefix)})
	var keys []string
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, storageError(ctx, err)
		}
		for _, object := range page.Contents {
			if object.Key != nil {
				keys = append(keys, *object.Key)
			}
		}
	}
	return keys, nil
}

func (s *store) delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return storageError(ctx, err)
	}
	return nil
}

func storageError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
		return ctxErr
	}
	return errors.New("backup storage operation failed")
}

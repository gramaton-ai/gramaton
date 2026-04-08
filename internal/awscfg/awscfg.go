package awscfg

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

// Load builds an AWS config from the given parameters. If region or
// profile are non-empty, they override the defaults. If accessKeyIDEnv
// and secretKeyEnv name environment variables that are set, static
// credentials from those vars override the default credential chain.
func Load(ctx context.Context, region, profile, accessKeyIDEnv, secretKeyEnv string) (aws.Config, error) {
	var opts []func(*awsconfig.LoadOptions) error

	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	if profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
	}

	// Explicit env var credentials override the default chain.
	if accessKeyIDEnv != "" && secretKeyEnv != "" {
		akid := os.Getenv(accessKeyIDEnv)
		secret := os.Getenv(secretKeyEnv)
		if akid != "" && secret != "" {
			opts = append(opts, awsconfig.WithCredentialsProvider(
				credentials.NewStaticCredentialsProvider(akid, secret, ""),
			))
		}
	}

	// Use an HTTP client with a reasonable timeout. The AWS SDK's
	// default client has no timeout, which can hang indefinitely.
	opts = append(opts, awsconfig.WithHTTPClient(&http.Client{
		Timeout: 120 * time.Second,
	}))

	return awsconfig.LoadDefaultConfig(ctx, opts...)
}

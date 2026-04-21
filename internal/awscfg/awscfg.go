package awscfg

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
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

	// Override the default retryer (MaxAttempts=3) with a more
	// aggressive policy: 5 attempts with exponential backoff +
	// jitter and a longer max backoff. Bedrock Converse 429s are
	// routine under curation load; the default 3 attempts can
	// lose classification work on a contentious hour. The SDK
	// honors Retry-After and handles ThrottlingException /
	// RequestLimitExceeded automatically within this budget.
	// (P1-23.)
	opts = append(opts, awsconfig.WithRetryer(func() aws.Retryer {
		return retry.NewStandard(func(o *retry.StandardOptions) {
			o.MaxAttempts = 5
			o.MaxBackoff = 30 * time.Second
		})
	}))

	return awsconfig.LoadDefaultConfig(ctx, opts...)
}

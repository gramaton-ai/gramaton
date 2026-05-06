package awscfg

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/credentials"
	smithylogging "github.com/aws/smithy-go/logging"
)

// Load builds an AWS config from the given parameters. If region or
// profile are non-empty, they override the defaults. If accessKeyIDEnv
// and secretKeyEnv name environment variables that are set, static
// credentials from those vars override the default credential chain.
//
// Logging:
//   - Debug: a one-line summary of which config knobs were applied
//     (region, whether a profile was named, whether explicit env-var
//     creds resolved). Useful for diagnosing "why is the SDK using
//     this account" without the value-leaking risk of dumping creds.
//   - SDK-internal: retry/refresh/signing events are routed through
//     the slog handler at Debug. ClientLogMode is configured for
//     LogRetries + LogDeprecatedUsage; request/response bodies are
//     NOT logged because they may contain credential material.
func Load(ctx context.Context, region, profile, accessKeyIDEnv, secretKeyEnv string) (aws.Config, error) {
	var opts []func(*awsconfig.LoadOptions) error
	var staticCredsResolved bool

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
			staticCredsResolved = true
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
	opts = append(opts, awsconfig.WithRetryer(func() aws.Retryer {
		return retry.NewStandard(func(o *retry.StandardOptions) {
			o.MaxAttempts = 5
			o.MaxBackoff = 30 * time.Second
		})
	}))

	// Wire the SDK's internal logger through slog so retries,
	// credential-refresh attempts, and signing diagnostics land
	// alongside the rest of Gramaton's logs at Debug. Avoids two
	// separate log streams (slog + raw stderr from the SDK).
	opts = append(opts,
		awsconfig.WithLogger(slogSDKLogger{}),
		awsconfig.WithClientLogMode(aws.LogRetries|aws.LogDeprecatedUsage),
	)

	slog.Debug("aws: loading config",
		"component", "awscfg",
		"region", region,
		"profile_set", profile != "",
		"static_creds_resolved", staticCredsResolved,
		"access_key_id_env_set", accessKeyIDEnv != "",
		"secret_key_env_set", secretKeyEnv != "")

	return awsconfig.LoadDefaultConfig(ctx, opts...)
}

// slogSDKLogger adapts the AWS SDK's smithy-go logging.Logger
// interface onto slog. SDK Warn classifications log at slog.Warn so
// throttling-class signals surface in the normal log; everything
// else (Debug, retries, signing, credential refresh) lands at
// slog.Debug. Component is consistent ("aws-sdk") so a downstream
// log filter can scope to SDK chatter.
type slogSDKLogger struct{}

func (slogSDKLogger) Logf(c smithylogging.Classification, format string, v ...any) {
	msg := fmt.Sprintf(format, v...)
	switch c {
	case smithylogging.Warn:
		slog.Warn("aws-sdk", "component", "aws-sdk", "msg", msg)
	default:
		slog.Debug("aws-sdk", "component", "aws-sdk", "classification", string(c), "msg", msg)
	}
}

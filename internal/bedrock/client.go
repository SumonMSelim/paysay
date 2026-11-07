// Package bedrock wraps the Amazon Bedrock Runtime Converse API for PaySay's
// agent. It exposes a small Converser interface so the agent loop can be tested
// against a fake model, and a concrete AWS-backed implementation.
package bedrock

import (
	"context"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
)

// Converser is the capability the agent needs from Bedrock: a single Converse
// call. The agent runs a multi-turn tool loop where each turn is a complete
// Converse response.
type Converser interface {
	Converse(ctx context.Context, in *bedrockruntime.ConverseInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error)
}

// Client is the AWS-backed Converser.
type Client struct {
	api    Converser
	region string
}

// New constructs a Bedrock client using the default AWS credential chain
// (environment, shared config, SSO, instance role). The region is applied on
// top of the loaded config.
func New(ctx context.Context, region string) (*Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, err
	}
	return &Client{
		api:    bedrockruntime.NewFromConfig(cfg),
		region: region,
	}, nil
}

// NewWithAPI wraps an existing Converser (used in tests).
func NewWithAPI(api Converser) *Client {
	return &Client{api: api}
}

// Converse delegates to the underlying API.
func (c *Client) Converse(ctx context.Context, in *bedrockruntime.ConverseInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	return c.api.Converse(ctx, in, optFns...)
}

// Region returns the configured AWS region.
func (c *Client) Region() string { return c.region }

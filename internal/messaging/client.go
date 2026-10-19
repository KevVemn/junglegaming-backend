package messaging

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/KevVemn/junglegaming-backend/internal/config"
)

type Client struct {
	sqs *sqs.Client
	cfg config.SQSConfig
}

func NewClient(ctx context.Context, cfg config.SQSConfig) (*Client, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
	}

	if cfg.EndpointURL != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider("test", "test", ""),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("loading aws config: %w", err)
	}

	client := sqs.NewFromConfig(awsCfg, func(o *sqs.Options) {
		if cfg.EndpointURL != "" {
			o.BaseEndpoint = aws.String(cfg.EndpointURL)
		}
	})

	return &Client{sqs: client, cfg: cfg}, nil
}

func (c *Client) SQS() *sqs.Client { return c.sqs }

func (c *Client) Ping(ctx context.Context) error {
	_, err := c.sqs.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(c.cfg.QueueURL),
	})
	if err != nil {
		return fmt.Errorf("sqs unreachable: %w", err)
	}
	return nil
}

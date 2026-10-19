#!/bin/bash
set -e

REGION="${AWS_DEFAULT_REGION:-us-east-1}"

create_fifo () {
  awslocal sqs create-queue --region "$REGION" --queue-name "$1" \
    --attributes "FifoQueue=true,ContentBasedDeduplication=false,VisibilityTimeout=30,MessageRetentionPeriod=1209600" >/dev/null
}

create_fifo wager-transactions-dlq.fifo
create_fifo wager-transactions.fifo
create_fifo wager-events.fifo

DLQ_ARN=$(awslocal sqs get-queue-attributes --region "$REGION" \
  --queue-url "http://localhost:4566/000000000000/wager-transactions-dlq.fifo" \
  --attribute-names QueueArn --query 'Attributes.QueueArn' --output text)

awslocal sqs set-queue-attributes --region "$REGION" \
  --queue-url "http://localhost:4566/000000000000/wager-transactions.fifo" \
  --attributes "{\"RedrivePolicy\":\"{\\\"deadLetterTargetArn\\\":\\\"${DLQ_ARN}\\\",\\\"maxReceiveCount\\\":\\\"5\\\"}\"}"

echo "sqs ready"

# Messaging topology (SQS/SNS) and broker-level least privilege (IAM).
#
#   wager-transactions.fifo  ingress (providers -> service), redrive to wager-transactions-dlq.fifo
#   wager-events.fifo        SNS FIFO topic for integration events published by the outbox relay
#   wager-events-audit.fifo  sample subscriber queue (raw delivery), redrive to its own DLQ
#
# Principals (one per component, separate access keys):
#   jungle-consumer          receive/delete/visibility on ingress; send to ingress DLQ (poison routing)
#   jungle-outbox-publisher  publish to the events topic
#   jungle-events-reader     consume the audit queue (tests, demos)
#   jungle-producer-<id>     send to ingress, one per provider (sender identity -> providerId)
terraform {
  required_providers {
    aws = { source = "hashicorp/aws" }
  }
}

locals {
  fifo_dlq_retention = 1209600 # 14 days
}

# ---------- ingress ----------
resource "aws_sqs_queue" "ingress_dlq" {
  name                        = "wager-transactions-dlq.fifo"
  fifo_queue                  = true
  content_based_deduplication = false
  message_retention_seconds   = local.fifo_dlq_retention
}

resource "aws_sqs_queue" "ingress" {
  name                        = "wager-transactions.fifo"
  fifo_queue                  = true
  content_based_deduplication = false
  deduplication_scope         = "messageGroup"
  fifo_throughput_limit       = "perMessageGroupId"
  visibility_timeout_seconds  = var.ingress_visibility_timeout_seconds
  receive_wait_time_seconds   = 20
  message_retention_seconds   = 345600 # 4 days
  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.ingress_dlq.arn
    maxReceiveCount     = var.ingress_max_receive_count
  })
}

# ---------- integration events ----------
resource "aws_sns_topic" "events" {
  name                        = "wager-events.fifo"
  fifo_topic                  = true
  content_based_deduplication = false
}

resource "aws_sqs_queue" "audit_dlq" {
  name                        = "wager-events-audit-dlq.fifo"
  fifo_queue                  = true
  content_based_deduplication = false
  message_retention_seconds   = local.fifo_dlq_retention
}

# Sample subscriber, read only by tests/demos: short retention keeps it bounded.
resource "aws_sqs_queue" "audit" {
  name                        = "wager-events-audit.fifo"
  fifo_queue                  = true
  content_based_deduplication = false
  visibility_timeout_seconds  = 30
  message_retention_seconds   = 3600
  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.audit_dlq.arn
    maxReceiveCount     = 5
  })
}

resource "aws_sqs_queue_policy" "audit_from_topic" {
  queue_url = aws_sqs_queue.audit.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "sns.amazonaws.com" }
      Action    = "sqs:SendMessage"
      Resource  = aws_sqs_queue.audit.arn
      Condition = { ArnEquals = { "aws:SourceArn" = aws_sns_topic.events.arn } }
    }]
  })
}

resource "aws_sns_topic_subscription" "audit" {
  topic_arn            = aws_sns_topic.events.arn
  protocol             = "sqs"
  endpoint             = aws_sqs_queue.audit.arn
  raw_message_delivery = true
}

# ---------- principals ----------
locals {
  ingress_send = {
    Effect   = "Allow"
    Action   = ["sqs:SendMessage", "sqs:GetQueueUrl", "sqs:GetQueueAttributes"]
    Resource = [aws_sqs_queue.ingress.arn]
  }
  policies = merge(
    {
      "jungle-consumer" = [
        {
          Effect   = "Allow"
          Action   = ["sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:ChangeMessageVisibility", "sqs:GetQueueUrl", "sqs:GetQueueAttributes"]
          Resource = [aws_sqs_queue.ingress.arn]
        },
        {
          Effect   = "Allow"
          Action   = ["sqs:SendMessage", "sqs:GetQueueUrl", "sqs:GetQueueAttributes"]
          Resource = [aws_sqs_queue.ingress_dlq.arn]
        },
      ]
      # AWS authorizes PublishBatch through sns:Publish; MiniStack (AUTH=true) checks
      # sns:PublishBatch explicitly, so both are listed.
      "jungle-outbox-publisher" = [{
        Effect   = "Allow"
        Action   = ["sns:Publish", "sns:PublishBatch", "sns:GetTopicAttributes"]
        Resource = [aws_sns_topic.events.arn]
      }]
      "jungle-events-reader" = [{
        Effect   = "Allow"
        Action   = ["sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:ChangeMessageVisibility", "sqs:GetQueueUrl", "sqs:GetQueueAttributes"]
        Resource = [aws_sqs_queue.audit.arn, aws_sqs_queue.audit_dlq.arn, aws_sqs_queue.ingress_dlq.arn]
      }]
    },
    { for p in var.game_providers : "jungle-producer-${p}" => [local.ingress_send] },
  )
  producer_of = { for p in var.game_providers : "jungle-producer-${p}" => p }
}

resource "aws_iam_user" "principal" {
  for_each = local.policies
  name     = each.key
}

resource "aws_iam_user_policy" "principal" {
  for_each = local.policies
  name     = "${each.key}-policy"
  user     = aws_iam_user.principal[each.key].name
  policy   = jsonencode({ Version = "2012-10-17", Statement = each.value })
}

resource "aws_iam_access_key" "principal" {
  for_each = local.policies
  user     = aws_iam_user.principal[each.key].name
}

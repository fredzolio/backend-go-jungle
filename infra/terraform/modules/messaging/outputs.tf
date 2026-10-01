output "queues" {
  value = {
    ingress     = { name = aws_sqs_queue.ingress.name, url = aws_sqs_queue.ingress.id, arn = aws_sqs_queue.ingress.arn }
    ingress_dlq = { name = aws_sqs_queue.ingress_dlq.name, url = aws_sqs_queue.ingress_dlq.id, arn = aws_sqs_queue.ingress_dlq.arn }
    audit       = { name = aws_sqs_queue.audit.name, url = aws_sqs_queue.audit.id, arn = aws_sqs_queue.audit.arn }
    audit_dlq   = { name = aws_sqs_queue.audit_dlq.name, url = aws_sqs_queue.audit_dlq.id, arn = aws_sqs_queue.audit_dlq.arn }
  }
}

output "events_topic_arn" {
  value = aws_sns_topic.events.arn
}

output "credentials" {
  description = "principal => { access_key_id, secret_access_key, user_id }"
  value = {
    for name, key in aws_iam_access_key.principal : name => {
      access_key_id     = key.id
      secret_access_key = key.secret
      user_id           = aws_iam_user.principal[name].unique_id
    }
  }
  sensitive = true
}

output "producer_of" {
  description = "principal => providerId, for mapping the SQS sender identity to a provider."
  value       = local.producer_of
}

output "principals" {
  description = "IAM principal names (non-sensitive, usable as for_each keys)."
  value       = toset(keys(local.policies))
}

output "api_url" {
  description = "Public ALB URL for the leaderboard API"
  value       = "http://${aws_lb.this.dns_name}"
}

output "redis_endpoint" {
  description = "ElastiCache primary endpoint"
  value       = aws_elasticache_replication_group.this.primary_endpoint_address
}

output "kinesis_stream" {
  value = aws_kinesis_stream.ingest.name
}

output "dynamodb_table" {
  value = aws_dynamodb_table.scores.name
}

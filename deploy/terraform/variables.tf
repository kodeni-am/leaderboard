variable "region" {
  description = "AWS region"
  type        = string
  default     = "us-east-1"
}

variable "name" {
  description = "Resource name prefix"
  type        = string
  default     = "openleaderboard"
}

variable "image" {
  description = "Container image for leaderboardd (e.g. <acct>.dkr.ecr.<region>.amazonaws.com/openleaderboard:latest)"
  type        = string
}

variable "admin_token" {
  description = "Admin token for app creation (store in Secrets Manager in production)"
  type        = string
  sensitive   = true
}

variable "signing_secret" {
  description = "Optional HMAC signing secret; empty disables submission verification"
  type        = string
  default     = ""
  sensitive   = true
}

variable "desired_count" {
  description = "Number of Fargate tasks"
  type        = number
  default     = 2
}

variable "cache_node_type" {
  description = "ElastiCache node type"
  type        = string
  default     = "cache.r6g.large"
}

variable "cache_replicas" {
  description = "Read replicas per shard"
  type        = number
  default     = 1
}

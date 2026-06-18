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

variable "public_url" {
  description = "Public origin for account email links/cookies (e.g. https://lb.example.com)"
  type        = string
}

variable "smtp_host" {
  description = "SMTP host for account verification / password-reset email"
  type        = string
}

variable "smtp_port" {
  description = "SMTP port"
  type        = number
  default     = 587
}

variable "smtp_user" {
  description = "SMTP username"
  type        = string
  default     = ""
}

variable "smtp_pass" {
  description = "SMTP password (store in Secrets Manager in production)"
  type        = string
  default     = ""
  sensitive   = true
}

variable "smtp_from" {
  description = "From address for transactional email"
  type        = string
  default     = "no-reply@example.com"
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

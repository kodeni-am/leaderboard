# OpenLeaderboard AWS reference architecture (Approach B).
#
# NOTE: This is a reviewed scaffold, not apply-tested in this repo's CI. Review
# IAM scoping, networking, and HA settings before production use. It uses the
# account's default VPC/subnets to stay self-contained; swap in a dedicated VPC
# module for real deployments.
#
# Components:
#   - ElastiCache (Redis/Valkey) replication group ......... ranking tier (cache)
#   - Kinesis stream ....................................... durable ingest log
#   - DynamoDB table ....................................... durable score store / tenancy
#   - ECS Fargate service behind an ALB .................... the leaderboardd API
#
# The app's Log interface currently ships Redis-Streams + in-memory backends;
# wiring KinesisLog (provisioned here) is the documented follow-on.

data "aws_vpc" "default" {
  default = true
}

data "aws_subnets" "default" {
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.default.id]
  }
}

locals {
  tags = {
    Project = var.name
    Stack   = "openleaderboard"
  }
}

############################
# Security groups
############################

resource "aws_security_group" "alb" {
  name_prefix = "${var.name}-alb-"
  vpc_id      = data.aws_vpc.default.id
  ingress {
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
  tags = local.tags
}

resource "aws_security_group" "service" {
  name_prefix = "${var.name}-svc-"
  vpc_id      = data.aws_vpc.default.id
  ingress {
    from_port       = 8080
    to_port         = 8080
    protocol        = "tcp"
    security_groups = [aws_security_group.alb.id]
  }
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
  tags = local.tags
}

resource "aws_security_group" "cache" {
  name_prefix = "${var.name}-cache-"
  vpc_id      = data.aws_vpc.default.id
  ingress {
    from_port       = 6379
    to_port         = 6379
    protocol        = "tcp"
    security_groups = [aws_security_group.service.id]
  }
  tags = local.tags
}

############################
# Ranking tier: ElastiCache
############################

resource "aws_elasticache_subnet_group" "this" {
  name       = "${var.name}-cache"
  subnet_ids = data.aws_subnets.default.ids
}

resource "aws_elasticache_replication_group" "this" {
  replication_group_id       = "${var.name}-ranking"
  description                = "OpenLeaderboard ranking tier (rebuildable cache)"
  engine                     = "redis"
  node_type                  = var.cache_node_type
  num_cache_clusters         = 1 + var.cache_replicas
  automatic_failover_enabled = var.cache_replicas > 0
  port                       = 6379
  subnet_group_name          = aws_elasticache_subnet_group.this.name
  security_group_ids         = [aws_security_group.cache.id]
  tags                       = local.tags
}

############################
# Durable ingest log: Kinesis
############################

resource "aws_kinesis_stream" "ingest" {
  name = "${var.name}-ingest"
  stream_mode_details {
    stream_mode = "ON_DEMAND"
  }
  tags = local.tags
}

############################
# Durable store / tenancy: DynamoDB
############################

resource "aws_dynamodb_table" "scores" {
  name         = "${var.name}-scores"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "pk"
  range_key    = "sk"
  attribute {
    name = "pk"
    type = "S"
  }
  attribute {
    name = "sk"
    type = "S"
  }
  tags = local.tags
}

############################
# ALB
############################

resource "aws_lb" "this" {
  name               = "${var.name}-alb"
  load_balancer_type = "application"
  security_groups    = [aws_security_group.alb.id]
  subnets            = data.aws_subnets.default.ids
  tags               = local.tags
}

resource "aws_lb_target_group" "this" {
  name        = "${var.name}-tg"
  port        = 8080
  protocol    = "HTTP"
  target_type = "ip"
  vpc_id      = data.aws_vpc.default.id
  health_check {
    path                = "/healthz"
    healthy_threshold   = 2
    unhealthy_threshold = 3
    interval            = 10
    timeout             = 5
  }
  tags = local.tags
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.this.arn
  port              = 80
  protocol          = "HTTP"
  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.this.arn
  }
}

############################
# ECS Fargate
############################

resource "aws_ecs_cluster" "this" {
  name = var.name
  tags = local.tags
}

resource "aws_iam_role" "task_exec" {
  name_prefix = "${var.name}-exec-"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Action    = "sts:AssumeRole"
      Effect    = "Allow"
      Principal = { Service = "ecs-tasks.amazonaws.com" }
    }]
  })
  tags = local.tags
}

resource "aws_iam_role_policy_attachment" "task_exec" {
  role       = aws_iam_role.task_exec.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

resource "aws_cloudwatch_log_group" "this" {
  name              = "/ecs/${var.name}"
  retention_in_days = 14
  tags              = local.tags
}

resource "aws_ecs_task_definition" "this" {
  family                   = var.name
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = "1024"
  memory                   = "2048"
  execution_role_arn       = aws_iam_role.task_exec.arn

  container_definitions = jsonencode([{
    name         = "leaderboardd"
    image        = var.image
    essential    = true
    portMappings = [{ containerPort = 8080, protocol = "tcp" }]
    environment = [
      { name = "REDIS_ADDR", value = "${aws_elasticache_replication_group.this.primary_endpoint_address}:6379" },
      { name = "LISTEN_ADDR", value = ":8080" },
      { name = "LB_LOG_BACKEND", value = "redis" },
      { name = "PUBLIC_URL", value = var.public_url },
      { name = "SECURE_COOKIES", value = "true" },
      { name = "SMTP_HOST", value = var.smtp_host },
      { name = "SMTP_PORT", value = tostring(var.smtp_port) },
      { name = "SMTP_USER", value = var.smtp_user },
      { name = "SMTP_PASS", value = var.smtp_pass },
      { name = "SMTP_FROM", value = var.smtp_from },
      { name = "SIGNING_SECRET", value = var.signing_secret },
      { name = "LB_REAPER_RETAIN", value = "168h" },
    ]
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        "awslogs-group"         = aws_cloudwatch_log_group.this.name
        "awslogs-region"        = var.region
        "awslogs-stream-prefix" = "leaderboardd"
      }
    }
  }])
  tags = local.tags
}

resource "aws_ecs_service" "this" {
  name            = var.name
  cluster         = aws_ecs_cluster.this.id
  task_definition = aws_ecs_task_definition.this.arn
  desired_count   = var.desired_count
  launch_type     = "FARGATE"

  network_configuration {
    subnets          = data.aws_subnets.default.ids
    security_groups  = [aws_security_group.service.id]
    assign_public_ip = true
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.this.arn
    container_name   = "leaderboardd"
    container_port   = 8080
  }

  depends_on = [aws_lb_listener.http]
  tags       = local.tags
}

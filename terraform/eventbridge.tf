# EventBridge Scheduler (not classic Events) — supports sub-minute rates and
# is the modern recommended path for cron/rate triggers to Lambda.

resource "aws_iam_role" "scheduler" {
  name = "${var.name_prefix}-scheduler"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "scheduler.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
  tags = local.default_tags
}

resource "aws_iam_role_policy" "scheduler" {
  name = "${var.name_prefix}-scheduler"
  role = aws_iam_role.scheduler.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "lambda:InvokeFunction"
      Resource = aws_lambda_function.publisher.arn
    }]
  })
}

resource "aws_scheduler_schedule" "publisher_tick" {
  name                         = "${var.name_prefix}-publisher-tick"
  schedule_expression          = var.schedule_expression
  schedule_expression_timezone = "Etc/UTC"
  state                        = "ENABLED"

  flexible_time_window {
    mode = "OFF" # fire exactly on schedule
  }

  target {
    arn      = aws_lambda_function.publisher.arn
    role_arn = aws_iam_role.scheduler.arn
  }
}

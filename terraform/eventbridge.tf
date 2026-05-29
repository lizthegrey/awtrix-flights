# EventBridge fires the publisher Lambda once per minute. The Lambda
# detects an EventBridge-shaped event and fans out 4 SQS messages with
# DelaySeconds = 0/15/30/45. Those messages trigger the same Lambda
# (via SQS event source mapping) at ~15-second cadence. This is the
# only way to get sub-minute scheduling on AWS — EventBridge Scheduler
# rate() expressions ignore the seconds component of start_date.

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
    mode = "OFF"
  }

  target {
    arn      = aws_lambda_function.publisher.arn
    role_arn = aws_iam_role.scheduler.arn
  }
}

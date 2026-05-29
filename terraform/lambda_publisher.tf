resource "aws_iam_role" "publisher" {
  name = "${var.name_prefix}-publisher"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "lambda.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
  tags = local.default_tags
}

resource "aws_iam_role_policy_attachment" "publisher_basic_logs" {
  role       = aws_iam_role.publisher.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

resource "aws_iam_role_policy" "publisher_inline" {
  name = "${var.name_prefix}-publisher"
  role = aws_iam_role.publisher.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Action = [
          "dynamodb:GetItem",
          "dynamodb:PutItem",
        ]
        Resource = aws_dynamodb_table.state.arn
      },
      {
        Effect   = "Allow"
        Action   = "iot:Publish"
        Resource = "arn:aws:iot:${local.region}:${local.account_id}:topic/${var.awtrix_topic}"
      },
    ]
  })
}

resource "aws_cloudwatch_log_group" "publisher" {
  name              = "/aws/lambda/${var.name_prefix}-publisher"
  retention_in_days = 14
  tags              = local.default_tags
}

resource "aws_lambda_function" "publisher" {
  function_name = "${var.name_prefix}-publisher"
  role          = aws_iam_role.publisher.arn
  runtime       = "provided.al2023"
  architectures = ["arm64"]
  handler       = "bootstrap"
  memory_size   = 256
  timeout       = 15

  filename         = data.archive_file.publisher.output_path
  source_code_hash = data.archive_file.publisher.output_base64sha256

  environment {
    variables = {
      TABLE_NAME        = aws_dynamodb_table.state.name
      MQTT_TOPIC        = var.awtrix_topic
      IOT_ENDPOINT      = data.aws_iot_endpoint.ats.endpoint_address
      HOME_LAT          = format("%.6f", var.home_lat)
      HOME_LON          = format("%.6f", var.home_lon)
      ICON_ID           = var.awtrix_icon_id
      LOG_LEVEL         = var.publisher_log_level
      HONEYCOMB_API_KEY = var.honeycomb_api_key
    }
  }

  depends_on = [
    aws_cloudwatch_log_group.publisher,
    aws_iam_role_policy.publisher_inline,
    aws_iam_role_policy_attachment.publisher_basic_logs,
  ]

  tags = local.default_tags
}

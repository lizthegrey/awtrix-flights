resource "aws_iam_role" "authorizer" {
  name = "${var.name_prefix}-authorizer"
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

resource "aws_iam_role_policy_attachment" "authorizer_basic_logs" {
  role       = aws_iam_role.authorizer.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

resource "aws_iam_role_policy" "authorizer_inline" {
  name = "${var.name_prefix}-authorizer"
  role = aws_iam_role.authorizer.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "secretsmanager:GetSecretValue"
      Resource = aws_secretsmanager_secret.mqtt.arn
    }]
  })
}

resource "aws_cloudwatch_log_group" "authorizer" {
  name              = "/aws/lambda/${var.name_prefix}-authorizer"
  retention_in_days = 14
  tags              = local.default_tags
}

resource "aws_lambda_function" "authorizer" {
  function_name = "${var.name_prefix}-authorizer"
  role          = aws_iam_role.authorizer.arn
  runtime       = "provided.al2023"
  architectures = ["arm64"]
  handler       = "bootstrap"
  memory_size   = 128
  timeout       = 5

  filename         = data.archive_file.authorizer.output_path
  source_code_hash = data.archive_file.authorizer.output_base64sha256

  environment {
    variables = {
      SECRET_NAME       = aws_secretsmanager_secret.mqtt.name
      AWS_ACCOUNT_ID    = local.account_id
      ALLOWED_CLIENT_ID = var.awtrix_client_id
      ALLOWED_TOPIC     = var.awtrix_topic
      HONEYCOMB_API_KEY = var.honeycomb_api_key
    }
  }

  depends_on = [
    aws_cloudwatch_log_group.authorizer,
    aws_iam_role_policy.authorizer_inline,
    aws_iam_role_policy_attachment.authorizer_basic_logs,
  ]

  tags = local.default_tags
}

# Allow IoT Core to invoke the authorizer Lambda.
resource "aws_lambda_permission" "authorizer_iot" {
  statement_id  = "AllowIoTInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.authorizer.function_name
  principal     = "iot.amazonaws.com"
  source_arn    = aws_iot_authorizer.mqtt.arn
}
